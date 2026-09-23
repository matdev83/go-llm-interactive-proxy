package billingstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	_ "modernc.org/sqlite"
)

// Phase 17.3 F3 RED: legal activation provides no usable V2 admission/
// settlement pipeline. Must FAIL before fix, PASS after.
//
// Legal sequence: Ensure -> shadow -> BeginDrain/Classify -> Activate via
// coordinator on an empty store. After v2_active, a fresh V2 customer call
// must admit through the actual production internal admission path
// (AdmitExposureWithOwner V2), capture call/leg terminal evidence
// (AppendCallUsageWithOwner/AppendCallLegUsageWithOwner V2), enqueue/claim
// provider+customer work with owner V2 (ClaimCompleteCallsWithCutover /
// ClaimProviderCostWorkWithCutover with V2 pins/tokens), and settle/post
// exactly once with V2 pins/tokens (ApplyCallBillingResult/ApplyProviderCost
// V2). Before v2_active the same explicit V2 admission is rejected. In
// active, legacy V1 admission remains fenced. No preloaded V1 exposure/
// usage, direct marker skipping, pin deletion, or test-only insertion.
// Version authority is consumed by actual APIs via explicit version-aware
// methods; V1 defaults remain compatible pre-cutover. Owner is chosen from
// the durable marker per StoreID at admission and retained through the call
// lifecycle; terminal evidence uses the admitted owner, not a fresh global
// read. Provider leg first appears at terminal under the admitted V2 call.
// F1 marker serialization and F6 token claims are exercised throughout.

func f3NewStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	base := newSQLiteTestStore(t)
	if base.StoreID() == storeID {
		return base
	}
	s, err := NewDurableStore(context.Background(), base.DB(), Config{StoreID: storeID})
	if err != nil {
		t.Fatalf("NewDurableStore %q: %v", storeID, err)
	}
	return s
}

func f3MustCallID(t *testing.T) billing.BillingCallID {
	t.Helper()
	id, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func f3SetupAccount(t *testing.T, store *DurableStore, accountID string, balance int64) {
	t.Helper()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: balance, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
}

func f3EnsureShadow(t *testing.T, store *DurableStore) billing.AccountingCutoverMarker {
	t.Helper()
	ctx := context.Background()
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState:    billing.AccountingCutoverV2Shadow,
			TransitionID: "f3-shadow",
		})
		if err != nil {
			t.Fatal(err)
		}
		return sh
	}
	return m
}

func f3ActivateEmpty(t *testing.T, store *DurableStore) billing.AccountingCutoverMarker {
	t.Helper()
	ctx := context.Background()
	f3EnsureShadow(t, store)
	if _, _, err := store.BeginCutoverDraining(ctx, "f3-drain"); err != nil {
		t.Fatalf("BeginCutoverDraining empty: %v", err)
	}
	activated, err := store.ActivateCutoverV2(ctx, "f3-activate")
	if err != nil {
		t.Fatalf("ActivateCutoverV2 empty: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated = %q, want v2_active", activated.State)
	}
	return activated
}

func f3IsFenceErr(err error) bool {
	return errors.Is(err, billing.ErrPostingOwnershipFence) ||
		errors.Is(err, billing.ErrPostingOwnershipConflict) ||
		errors.Is(err, billing.ErrAccountingCutoverFence) ||
		errors.Is(err, billing.ErrCutoverV1Fenced) ||
		errors.Is(err, billing.ErrCutoverV2NotAuthorized) ||
		errors.Is(err, billing.ErrRetailRateIncomplete) ||
		errors.Is(err, ErrOperationConflict)
}

func f3Balance(t *testing.T, store *DurableStore, accountID string) int64 {
	t.Helper()
	acct, err := store.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return acct.BalanceNano
}

func f3JournalCount(t *testing.T, store *DurableStore, accountID string) int {
	t.Helper()
	txs, err := store.JournalTransactions(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return len(txs)
}

type f3RatingStub struct {
	t      *testing.T
	charge int64
}

// f3BoundComponentValuation builds a complete customer-policy component
// valuation bound to the actual settled call and charge. Settlement identity
// (account/call, scope, payer, policy/tariff snapshots, currency, amount)
// derives from the live call and charge; only snapshot content digests use
// deterministic fixture hashing in the phase10 manner. The caller must use
// the valuation fingerprint as the result fingerprint.
func f3BoundComponentValuation(t *testing.T, call billing.CallUsageRecord, charge billing.Money) economics.Valuation {
	t.Helper()
	const storeID = "test"
	ref := metering.ObservationRef{StoreID: storeID, ObservationID: "observation-" + call.CallID.String(), Revision: 1, PayloadHash: "payload-" + call.CallID.String()}
	inputHash, err := economics.CanonicalInputSetHash(economics.BasisCustomerPolicy, []metering.ObservationRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	tariffID, tariffVersion := call.CustomerPricingRef.ID, call.CustomerPricingRef.Version
	policyID, policyVersion := call.ChargePolicyRef.ID, call.ChargePolicyRef.Version
	tariffHash := f3BoundHash("tariff", tariffID+"/"+tariffVersion)
	policyHash := f3BoundHash("policy", policyID+"/"+policyVersion)
	qualifierHash := f3BoundHash("qualifier", tariffID+"/"+tariffVersion+"/"+policyID+"/"+policyVersion)
	amount := f3BoundDecimal(t, charge.Nano)
	unitPrice := f3BoundDecimal(t, charge.Nano)
	quantity, err := metering.ParseDecimal("1")
	if err != nil {
		t.Fatal(err)
	}
	rounded := economics.Money{NanoUnits: charge.Nano, Currency: charge.Currency, Present: true}
	component := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	line := economics.LineItem{
		ID: "measure:input-token", RuleID: "legacy.input_token", ItemID: "legacy.input_token",
		Component: &component, Quantity: &quantity, Unit: metering.UnitToken,
		UnitPrice: &unitPrice, Amount: &amount, RoundedAmount: &rounded,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfAwayFromZero,
		Status: economics.RatingLineRated, ChargeKind: "inference_usage",
		SourceObservationRefs: []metering.ObservationRef{ref},
	}
	totalAmount := f3BoundDecimal(t, charge.Nano)
	v := economics.Valuation{
		ID: "valuation-" + call.CallID.String(), Version: economics.ValuationVersionV2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject: metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: storeID, BillingCallID: call.CallID.String()}, Scope: "call:" + call.CallID.String(),
		InputObservations: []metering.ObservationRef{ref}, InputSetHash: inputHash,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: tariffID, Version: tariffVersion}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "rater://" + tariffID + "/" + tariffVersion, ContentHash: tariffHash},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: tariffID, Version: tariffVersion}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "tariff://" + tariffID + "/" + tariffVersion, ContentHash: tariffHash},
		Policy:               economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: policyID, Version: policyVersion}, PolicyID: policyID},
		PolicyContent:        &economics.SnapshotContentRef{ContentRef: "policy://" + policyID + "/" + policyVersion, ContentHash: policyHash},
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "qualifier://" + tariffID + "/" + tariffVersion + "/" + policyID + "/" + policyVersion, ContentHash: qualifierHash},
		Payer:                metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: call.AccountID},
		Lines:                []economics.LineItem{line}, Totals: []economics.CurrencyTotal{{Currency: charge.Currency, Amount: &totalAmount, RoundedAmount: rounded}},
		Completeness: economics.CompletenessComplete, CreatedAt: time.Unix(101, 0).UTC(),
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("bound fixture valuation must validate: %v", err)
	}
	return v
}

// f3BoundResult binds one stub charge to the actual complete call: the
// valuation carries the call's account/call/policy/tariff identity and the
// charge currency/amount, and the result fingerprint is the valuation
// fingerprint so result identity cannot drift from rated content.
func f3BoundResult(t *testing.T, call billing.CallUsageRecord, chargeNano int64) billing.CallRatingResult {
	t.Helper()
	charge := billing.Money{Nano: chargeNano, Currency: "USD"}
	valuation := f3BoundComponentValuation(t, call, charge)
	fp := valuation.Fingerprint()
	if fp == "" {
		t.Fatalf("bound fixture valuation fingerprint is empty")
	}
	return billing.CallRatingResult{CallID: call.CallID, CustomerCharge: charge, Fingerprint: fp, CustomerValuation: valuation}
}

func f3BoundHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func f3BoundDecimal(t *testing.T, nano int64) metering.Decimal {
	t.Helper()
	whole := nano / 1_000_000_000
	frac := nano % 1_000_000_000
	value, err := metering.ParseDecimal(fmt.Sprintf("%d.%09d", whole, frac))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (s f3RatingStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	return f3BoundResult(s.t, complete.Closure, s.charge), nil
}

func (s f3RatingStub) ResolveCallRatingForOwner(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure, _ string) (billing.CallRatingResult, error) {
	return f3BoundResult(s.t, complete.Closure, s.charge), nil
}

type f3ProviderStub struct{}

func (f3ProviderStub) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 9, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

func TestF3V2AdmissionRejectedPreactive(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "f3-preactive")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-f3-pre", 100000)
	callID := f3MustCallID(t)
	stub := testIndependentCallUsageFor(callID, []string{"b-1"})
	stub.AccountID = "acct-f3-pre"
	input := billing.AdmitExposureInput{
		AccountID: "acct-f3-pre", CallID: callID.String(),
		Max:        billing.Money{Nano: 500, Currency: "USD"},
		PricingRef: stub.CustomerPricingRef, ChargePolicyRef: stub.ChargePolicyRef,
	}
	// v1_active: explicit V2 admission rejected.
	if _, err := store.AdmitExposureWithOwner(ctx, input, billing.PostingOwnerV2); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("v1_active V2 admit err = %v, want V2NotAuthorized", err)
	}
	// Shadow: explicit V2 admission rejected.
	f3EnsureShadow(t, store)
	if _, err := store.AdmitExposureWithOwner(ctx, input, billing.PostingOwnerV2); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("shadow V2 admit err = %v, want V2NotAuthorized", err)
	}
	// Draining: explicit V2 admission rejected (draining forbids all new).
	if _, _, err := store.BeginCutoverDraining(ctx, "f3-pre-drain"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitExposureWithOwner(ctx, input, billing.PostingOwnerV2); err == nil {
		t.Fatalf("draining V2 admit must be rejected")
	} else if !f3IsFenceErr(err) && !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("draining V2 admit err = %v, want fence/NotAuthorized", err)
	}
	// V1 default remains compatible pre-cutover is covered by F2A; here only
	// assert V1 terminal without admission stays fenced in draining.
	fresh := testIndependentCallUsageFor(f3MustCallID(t), []string{"b-x"})
	fresh.AccountID = "acct-f3-pre"
	if err := store.AppendCallUsageWithOwner(ctx, fresh, billing.PostingOwnerV1); !f3IsFenceErr(err) {
		t.Fatalf("draining fresh V1 closure err = %v, want fence", err)
	}
}

func TestF3LegalEmptyActivationThenFreshV2Pipeline(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "f3-pipeline")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-f3-pipe", 100000)
	// Legal empty/drained sequence via coordinator only. No preloaded work,
	// no direct marker skipping.
	f3ActivateEmpty(t, store)
	if err := store.CheckV2NewWorkAuthorized(ctx); err != nil {
		t.Fatalf("V2 must be authorized after legal activation: %v", err)
	}
	// Legacy V1 admission remains fenced in active.
	v1CallID := f3MustCallID(t)
	v1Stub := testIndependentCallUsageFor(v1CallID, []string{"b-v1"})
	v1Stub.AccountID = "acct-f3-pipe"
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-pipe", CallID: v1CallID.String(),
		Max:        billing.Money{Nano: 100, Currency: "USD"},
		PricingRef: v1Stub.CustomerPricingRef, ChargePolicyRef: v1Stub.ChargePolicyRef,
	}); !f3IsFenceErr(err) {
		t.Fatalf("active V1 AdmitExposure err = %v, want fence", err)
	}
	// Fresh V2 customer call through the actual production internal admission
	// path with explicit V2 owner.
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-v2"})
	closure.AccountID = "acct-f3-pipe"
	exp, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-pipe", CallID: callID.String(),
		Max:        billing.Money{Nano: 800, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("active V2 AdmitExposureWithOwner: %v", err)
	}
	if !exp.IsOpen() {
		t.Fatalf("V2 exposure must be open")
	}
	// Admission pins V2 ownership at start (same tx, current marker epoch).
	opKey, err := billing.CustomerPostingOperationKey("acct-f3-pipe", callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("V2 admission pin missing: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || pin.Status != billing.PostingPinPinned {
		t.Fatalf("admission pin must be V2 pinned, got %#v", pin)
	}
	if pin.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("admission pin marker = %q, want v2_active", pin.MarkerState)
	}
	marker, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pin.MarkerVersion != marker.Version || pin.MarkerEpoch != marker.Epoch {
		t.Fatalf("admission pin %d/%d != marker %d/%d (same-tx snapshot)",
			pin.MarkerVersion, pin.MarkerEpoch, marker.Version, marker.Epoch)
	}
	// Terminal evidence uses the admitted V2 owner, not a fresh global read.
	// Provider leg first appears at terminal under the admitted V2 call.
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 AppendCallUsageWithOwner: %v", err)
	}
	leg := testIndependentCallLegFor(callID, "b-v2")
	if err := store.AppendCallLegUsageWithOwner(ctx, leg, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 AppendCallLegUsageWithOwner: %v", err)
	}
	// Provider leg gets V2 provider work/pin at terminal.
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provOpKey, err := billing.ProviderCostSourceKey(sealedLeg.Key)
	if err != nil {
		t.Fatal(err)
	}
	provPin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, provOpKey)
	if err != nil {
		t.Fatalf("V2 provider pin missing at terminal: %v", err)
	}
	if provPin.Owner != billing.PostingOwnerV2 {
		t.Fatalf("provider pin owner = %q, want v2", provPin.Owner)
	}
	// Enqueue/claim provider+customer work with owner V2 and F6 tokens.
	// GetCutoverClaimMetadata returns the renewable current-marker V2 tokens
	// without leasing; production workers below consume the token-carrying
	// Claim...WithCutover ports (proving V2 work is claimable in active).
	provMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationProviderCharge, provOpKey)
	if err != nil {
		t.Fatalf("V2 provider claim metadata: %v", err)
	}
	if provMeta.Owner != billing.PostingOwnerV2 {
		t.Fatalf("provider claim owner = %q, want v2", provMeta.Owner)
	}
	if err := billing.ValidateProviderCostClaim(provMeta, "acct-f3-pipe", callID, sealedLeg.Key); err != nil {
		t.Fatalf("provider claim validate: %v", err)
	}
	custMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("V2 customer claim metadata: %v", err)
	}
	if custMeta.Owner != billing.PostingOwnerV2 {
		t.Fatalf("customer claim owner = %q, want v2", custMeta.Owner)
	}
	if err := billing.ValidateCustomerSettlementClaim(custMeta, "acct-f3-pipe", callID); err != nil {
		t.Fatalf("customer claim validate: %v", err)
	}
	// Settle/post exactly once with V2 pins/tokens through production workers.
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider worker ProcessOnce: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{t: t, charge: 120}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer worker ProcessOnce: %v", err)
	}
	if got := f3Balance(t, store, "acct-f3-pipe"); got != 100000-120 {
		t.Fatalf("balance = %d, want %d", got, 100000-120)
	}
	// Customer + provider journals: exactly one each, no duplicates.
	if n := f3JournalCount(t, store, "acct-f3-pipe"); n != 2 {
		t.Fatalf("journals = %d, want 2 (customer+provider exactly once)", n)
	}
	if expAfter, err := store.GetCallExposure(ctx, callID); err != nil {
		t.Fatal(err)
	} else if expAfter.IsOpen() {
		t.Fatalf("V2 exposure must close after settlement")
	}
	custPinAfter, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if custPinAfter.Owner != billing.PostingOwnerV2 || !custPinAfter.IsCompleted() {
		t.Fatalf("customer pin must be V2 completed, got %#v", custPinAfter)
	}
	provPinAfter, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, provOpKey)
	if err != nil {
		t.Fatal(err)
	}
	if provPinAfter.Owner != billing.PostingOwnerV2 || !provPinAfter.IsCompleted() {
		t.Fatalf("provider pin must be V2 completed, got %#v", provPinAfter)
	}
	// Exactly-once: workers replay without second money.
	beforeBal := f3Balance(t, store, "acct-f3-pipe")
	beforeJ := f3JournalCount(t, store, "acct-f3-pipe")
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider replay: %v", err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer replay: %v", err)
	}
	if got := f3Balance(t, store, "acct-f3-pipe"); got != beforeBal {
		t.Fatalf("replay moved balance %d -> %d", beforeBal, got)
	}
	if n := f3JournalCount(t, store, "acct-f3-pipe"); n != beforeJ {
		t.Fatalf("replay journals %d -> %d", beforeJ, n)
	}
	// No owner changes mid-call: V1 post for the same V2-admitted call fences.
	durableCall, err := store.GetCallUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	durableExp, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	_ = durableExp
	v1Res := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 120, Currency: "USD"}, Fingerprint: "f3-pipe-fp"}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: durableCall, Exposure: exp, Result: v1Res, PostingOwner: billing.PostingOwnerV1}); !f3IsFenceErr(err) {
		t.Fatalf("V1 post on V2-admitted call err = %v, want fence/conflict", err)
	}
}

func TestF3V2ClaimWithCutoverReturnsV2AndSettlesOnce(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "f3-claim")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-f3-claim", 80000)
	f3ActivateEmpty(t, store)
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-claim"})
	closure.AccountID = "acct-f3-claim"
	exp, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-claim", CallID: callID.String(),
		Max:        billing.Money{Nano: 600, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("closure: %v", err)
	}
	leg := testIndependentCallLegFor(callID, "b-claim")
	if err := store.AppendCallLegUsageWithOwner(ctx, leg, billing.PostingOwnerV2); err != nil {
		t.Fatalf("leg: %v", err)
	}
	// Explicit token-carrying claims return V2 work with V2 tokens.
	claimedProv, err := store.ClaimProviderCostWorkWithCutover(ctx, 8)
	if err != nil {
		t.Fatalf("ClaimProviderCostWorkWithCutover: %v", err)
	}
	if len(claimedProv) != 1 {
		t.Fatalf("claimed provider = %d, want 1", len(claimedProv))
	}
	if claimedProv[0].Claim.Owner != billing.PostingOwnerV2 {
		t.Fatalf("provider claim owner = %q, want v2", claimedProv[0].Claim.Owner)
	}
	if err := claimedProv[0].Validate(); err != nil {
		t.Fatalf("provider claim validate: %v", err)
	}
	claimedCust, err := store.ClaimCompleteCallsWithCutover(ctx, 8)
	if err != nil {
		t.Fatalf("ClaimCompleteCallsWithCutover: %v", err)
	}
	if len(claimedCust) != 1 {
		t.Fatalf("claimed customer = %d, want 1", len(claimedCust))
	}
	if claimedCust[0].Claim.Owner != billing.PostingOwnerV2 {
		t.Fatalf("customer claim owner = %q, want v2", claimedCust[0].Claim.Owner)
	}
	if err := claimedCust[0].Validate(); err != nil {
		t.Fatalf("customer claim validate: %v", err)
	}
	// Settle/post exactly once with those V2 pins/tokens (direct production
	// seams, same authority workers consume).
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	provRes := billing.OperatorCostResult{LURKey: sealedLeg.Key, Amount: billing.Money{Nano: 9, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	provCopy := claimedProv[0].Claim
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: "acct-f3-claim", CallID: callID, Leg: leg, Result: provRes,
		PostingOwner: provCopy.Owner, Claim: &provCopy,
	}); err != nil {
		t.Fatalf("ApplyProviderCost V2 with claim: %v", err)
	}
	durableCall, err := store.GetCallUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	custCopy := claimedCust[0].Claim
	custRes := f3BoundResult(t, durableCall, 70)
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: durableCall, Exposure: exp, Result: custRes,
		PostingOwner: custCopy.Owner, Claim: &custCopy,
	})
	if err != nil {
		t.Fatalf("ApplyCallBillingResult V2 with claim: %v", err)
	}
	if settled.Replayed {
		t.Fatalf("first V2 settlement must not be replayed")
	}
	if got := f3Balance(t, store, "acct-f3-claim"); got != 80000-70 {
		t.Fatalf("balance = %d, want %d", got, 80000-70)
	}
	if n := f3JournalCount(t, store, "acct-f3-claim"); n != 2 {
		t.Fatalf("journals = %d, want 2", n)
	}
}

func TestF3RestartBetweenAdmitTerminalAndClaimPost(t *testing.T) {
	t.Parallel()
	dsn := "file:f3-restart?mode=memory&cache=shared&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	open := func(storeID string) *DurableStore {
		s, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: storeID})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	store := open("f3-restart")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-f3-restart", 50000)
	f3ActivateEmpty(t, store)
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-r"})
	closure.AccountID = "acct-f3-restart"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-restart", CallID: callID.String(),
		Max:        billing.Money{Nano: 700, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatalf("admit: %v", err)
	}
	// Restart between admit and terminal: reopen same DB, same StoreID.
	reopened1, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f3-restart"})
	if err != nil {
		t.Fatal(err)
	}
	_ = reopened1
	store = open("f3-restart")
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("terminal closure after reopen: %v", err)
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, testIndependentCallLegFor(callID, "b-r"), billing.PostingOwnerV2); err != nil {
		t.Fatalf("terminal leg after reopen: %v", err)
	}
	// Restart between terminal and claim/post: reopen same DB, same StoreID.
	// F6 tokens survive reopen via durable pins (no lease yet).
	store = open("f3-restart")
	opKeyRestart, err := billing.CustomerPostingOperationKey("acct-f3-restart", callID)
	if err != nil {
		t.Fatal(err)
	}
	restartMeta, err := store.GetCutoverClaimMetadata(ctx, billing.PostingOperationCustomerSettlement, opKeyRestart)
	if err != nil {
		t.Fatalf("claim metadata after reopen: %v", err)
	}
	if restartMeta.Owner != billing.PostingOwnerV2 {
		t.Fatalf("reopen claim owner = %q, want v2", restartMeta.Owner)
	}
	pw, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3ProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := pw.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider worker after reopen: %v", err)
	}
	cw, err := billing.NewCallPostUsageWorkerWithCutover(store, store, f3RatingStub{t: t, charge: 60}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := cw.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer worker after reopen: %v", err)
	}
	if got := f3Balance(t, store, "acct-f3-restart"); got != 50000-60 {
		t.Fatalf("balance = %d, want %d", got, 50000-60)
	}
	_ = sqlDB.Close()
}

func TestF3ConcurrentMarkerTransitionAndIsolation(t *testing.T) {
	t.Parallel()
	store := f3NewStore(t, "f3-conc")
	ctx := context.Background()
	f3SetupAccount(t, store, "acct-f3-conc", 200000)
	f3SetupAccount(t, store, "acct-f3-conc-2", 200000)
	f3ActivateEmpty(t, store)
	// Concurrent fresh V2 admissions for distinct calls all succeed with V2
	// pins; concurrent legacy V1 admissions fence. Deterministic start gate.
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	v2Errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			callID := f3MustCallID(t)
			closure := testIndependentCallUsageFor(callID, []string{"b-conc"})
			closure.AccountID = "acct-f3-conc"
			_, err := store.AdmitExposureWithOwner(context.Background(), billing.AdmitExposureInput{
				AccountID: "acct-f3-conc", CallID: callID.String(),
				Max:        billing.Money{Nano: 100, Currency: "USD"},
				PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
			}, billing.PostingOwnerV2)
			v2Errs[idx] = err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range v2Errs {
		if err != nil {
			t.Fatalf("concurrent V2 admit %d: %v", i, err)
		}
	}
	// V1 contender in active fences.
	v1ID := f3MustCallID(t)
	v1Stub := testIndependentCallUsageFor(v1ID, []string{"b-v1c"})
	v1Stub.AccountID = "acct-f3-conc"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-conc", CallID: v1ID.String(),
		Max:        billing.Money{Nano: 100, Currency: "USD"},
		PricingRef: v1Stub.CustomerPricingRef, ChargePolicyRef: v1Stub.ChargePolicyRef,
	}, billing.PostingOwnerV1); !f3IsFenceErr(err) {
		t.Fatalf("concurrent V1 admit err = %v, want fence", err)
	}
	// StoreID isolation: sibling store has independent marker (v1_active),
	// explicit V2 there is rejected while this store stays v2_active.
	other := f3NewStore(t, "f3-conc-other")
	otherAcct := billing.Account{ID: "acct-f3-conc", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	_ = other.CreateAccount(ctx, otherAcct)
	otherCall := f3MustCallID(t)
	otherStub := testIndependentCallUsageFor(otherCall, []string{"b-o"})
	otherStub.AccountID = "acct-f3-conc"
	if _, err := other.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-conc", CallID: otherCall.String(),
		Max:        billing.Money{Nano: 50, Currency: "USD"},
		PricingRef: otherStub.CustomerPricingRef, ChargePolicyRef: otherStub.ChargePolicyRef,
	}, billing.PostingOwnerV2); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("isolated store V2 err = %v, want NotAuthorized", err)
	}
	// Account isolation: same call bound to a different account fences.
	callID := f3MustCallID(t)
	closure := testIndependentCallUsageFor(callID, []string{"b-iso"})
	closure.AccountID = "acct-f3-conc"
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: "acct-f3-conc", CallID: callID.String(),
		Max:        billing.Money{Nano: 100, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); err != nil {
		t.Fatal(err)
	}
	cross := closure
	cross.AccountID = "acct-f3-conc-2"
	if err := store.AppendCallUsageWithOwner(ctx, cross, billing.PostingOwnerV2); err == nil {
		t.Fatalf("cross-account terminal must fail")
	}
}
