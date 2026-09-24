package runtimebundle_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreRuntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	_ "modernc.org/sqlite"
)

// Phase 17.3 F3 runtimebundle certification: legal activation followed by a
// fresh V2 customer call through the actual production composition
// (ComposeBilling adapter chooses owner from the durable marker per StoreID,
// retains it via exposure+pin, terminal uses the admitted record, workers
// claim/post with V2 pins/tokens exactly once). No preloaded V1 work,
// direct marker skipping, pin deletion, or test-only insertion. Public
// pkg/lipruntime.Options stays non-money (untouched; composition injects
// billing internally via ComposeBilling).

func f3rbNewStore(t *testing.T, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := "file:f3rb-" + storeID + "?mode=memory&cache=shared&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func f3rbCompose(t *testing.T, store *billingstore.DurableStore) runtimebundle.ProductionOptions {
	t.Helper()
	catalog, _, _ := seededComposeCatalog(t)
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             store,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Currency:          "USD",
		ModelMaxOutput:    composeModelMax,
		PostTurnBatchSize: 8,
		Strict:            true,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}
	if prod.BillingExposureAdmission == nil || prod.BillingTerminalUsageSink == nil || prod.BillingStore == nil {
		t.Fatal("incomplete production composition")
	}
	return prod
}

func f3rbAdmissionInput(callID string, accountID string) coreRuntime.BillingExposureAdmissionInput {
	primary := routing.Primary{Backend: "openai-responses", Model: "gpt"}
	return coreRuntime.BillingExposureAdmissionInput{
		BillingAdmissionInput: coreRuntime.BillingAdmissionInput{
			Call:   lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-f3rb"}},
			ALegID: "a-f3rb", BillingCallID: callID,
			Route:       &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
			RequestSize: routing.RequestSizeEstimate{Available: true, Tokens: 1},
			Scope:       scope.PrincipalScopeView{PrincipalID: scope.Known(accountID)},
			SessionID:   "sess-f3rb",
			AccountID:   accountID,
		},
		CallID: callID,
	}
}

type f3rbProviderStub struct{}

func (f3rbProviderStub) ResolveProviderCost(_ context.Context, leg billing.CallLegUsageRecord) (billing.OperatorCostResult, error) {
	sealed, err := leg.Seal()
	if err != nil {
		return billing.OperatorCostResult{}, err
	}
	return billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
}

type f3rbRatingStub struct {
	t      *testing.T
	charge int64
}

// f3rbBoundComponentValuation builds a complete customer-policy component
// valuation bound to the actual settled call and charge. Settlement identity
// (account/call, scope, payer, policy/tariff snapshots, currency, amount)
// derives from the live call and charge; only snapshot content digests use
// deterministic fixture hashing in the phase10 manner. The caller must use
// the valuation fingerprint as the result fingerprint.
func f3rbBoundComponentValuation(t *testing.T, call billing.CallUsageRecord, charge billing.Money) economics.Valuation {
	t.Helper()
	const storeID = "test"
	ref := metering.ObservationRef{StoreID: storeID, ObservationID: "observation-" + call.CallID.String(), Revision: 1, PayloadHash: "payload-" + call.CallID.String()}
	inputHash, err := economics.CanonicalInputSetHash(economics.BasisCustomerPolicy, []metering.ObservationRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	tariffID, tariffVersion := call.CustomerPricingRef.ID, call.CustomerPricingRef.Version
	policyID, policyVersion := call.ChargePolicyRef.ID, call.ChargePolicyRef.Version
	tariffHash := f3rbBoundHash("tariff", tariffID+"/"+tariffVersion)
	policyHash := f3rbBoundHash("policy", policyID+"/"+policyVersion)
	qualifierHash := f3rbBoundHash("qualifier", tariffID+"/"+tariffVersion+"/"+policyID+"/"+policyVersion)
	amount := f3rbBoundDecimal(t, charge.Nano)
	unitPrice := f3rbBoundDecimal(t, charge.Nano)
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
	totalAmount := f3rbBoundDecimal(t, charge.Nano)
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

// f3rbBoundResult binds one stub charge to the actual complete call: the
// valuation carries the call's account/call/policy/tariff identity and the
// charge currency/amount, and the result fingerprint is the valuation
// fingerprint so result identity cannot drift from rated content.
func f3rbBoundResult(t *testing.T, call billing.CallUsageRecord, chargeNano int64) billing.CallRatingResult {
	t.Helper()
	charge := billing.Money{Nano: chargeNano, Currency: "USD"}
	valuation := f3rbBoundComponentValuation(t, call, charge)
	fp := valuation.Fingerprint()
	if fp == "" {
		t.Fatalf("bound fixture valuation fingerprint is empty")
	}
	return billing.CallRatingResult{CallID: call.CallID, CustomerCharge: charge, Fingerprint: fp, CustomerValuation: valuation}
}

func f3rbBoundHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func f3rbBoundDecimal(t *testing.T, nano int64) metering.Decimal {
	t.Helper()
	whole := nano / 1_000_000_000
	frac := nano % 1_000_000_000
	value, err := metering.ParseDecimal(fmt.Sprintf("%d.%09d", whole, frac))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (s f3rbRatingStub) ResolveCallRating(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure) (billing.CallRatingResult, error) {
	return f3rbBoundResult(s.t, complete.Closure, s.charge), nil
}

func (s f3rbRatingStub) ResolveCallRatingForOwner(_ context.Context, complete billing.CompleteCall, _ billing.CallExposure, _ string) (billing.CallRatingResult, error) {
	return f3rbBoundResult(s.t, complete.Closure, s.charge), nil
}

func TestF3RuntimeBundleV2PipelineAfterLegalActivation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "f3rb-pipe")
	prod := f3rbCompose(t, store)
	accountID := "acct-f3rb-pipe"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 90000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	// Legal empty/drained sequence via coordinator only.
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f3rb-shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = sh
	if _, _, err := store.BeginCutoverDraining(ctx, "f3rb-drain"); err != nil {
		t.Fatal(err)
	}
	activated, err := store.ActivateCutoverV2(ctx, "f3rb-activate")
	if err != nil {
		t.Fatalf("legal empty activation: %v", err)
	}
	if activated.State != billing.AccountingCutoverV2Active {
		t.Fatalf("activated = %q, want v2_active", activated.State)
	}
	// Legacy V1 store admission remains fenced in active.
	v1Call, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	v1Closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: v1Call, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v7"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    []string{"b-v1"},
	}
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: v1Call.String(),
		Max:        billing.Money{Nano: 50, Currency: "USD"},
		PricingRef: v1Closure.CustomerPricingRef, ChargePolicyRef: v1Closure.ChargePolicyRef,
	}); err == nil {
		t.Fatalf("active legacy V1 AdmitExposure must be fenced")
	}
	// Fresh V2 via the actual production admission path. The composed
	// adapter chooses V2 from the durable marker (no invented owner).
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	exp, err := prod.BillingExposureAdmission.Admit(ctx, f3rbAdmissionInput(callID.String(), accountID))
	if err != nil {
		t.Fatalf("production adapter Admit in active (auto-V2): %v", err)
	}
	if !exp.IsOpen() {
		t.Fatalf("V2 exposure must be open")
	}
	opKey, err := billing.CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatalf("V2 admission pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || pin.MarkerState != billing.AccountingCutoverV2Active {
		t.Fatalf("admission pin must be V2 active, got %#v", pin)
	}
	// Terminal evidence under the admitted V2 owner (provider leg first
	// appears at terminal). Uses the admitted record, not a fresh global.
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(201, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: exp.PricingRef, ChargePolicyRef: exp.ChargePolicyRef,
		ExpectedBLegIDs: []string{"b-f3rb"},
	}
	if err := store.AppendCallUsageWithOwner(ctx, closure, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 terminal closure: %v", err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-f3rb", BLegID: "b-f3rb", AttemptSeq: 1,
		BackendID: "openai-responses", ProviderID: "p", ModelID: "gpt",
		StartedAt: time.Unix(200, 0).UTC(), FinishedAt: time.Unix(200, 500000000).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			// Divergent V1 scalar tokens: if the scalar live engine were
			// selected, the charge would be 1M*100+1M*200+3=303. The V2
			// component path must rate from the canonical V2 quantities
			// below (5/2 tokens + fixed 3 ≈ 3 nanos), never from these.
			InputTokens: billing.Quantity{Value: 1_000_000, Present: true}, OutputTokens: billing.Quantity{Value: 1_000_000, Present: true},
			Cost:      billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:    billing.EvidenceSourceProviderReported,
			Authority: billing.EvidenceAuthorityAuthoritative, DedupeKey: "f3rb-charge-1",
		},
		OperatorRateRef:    billing.VersionRef{ID: "operator-rates", Version: "v1"},
		EvidenceVersion:    billing.EvidenceFormatVersionV2,
		EvidenceProjection: billing.EvidenceProjectionV1,
		Observations:       []metering.Observation{f3rbV2Observation(t, "f3rb-pipe", callID, "b-f3rb")},
	}
	if err := store.AppendCallLegUsageWithOwner(ctx, leg, billing.PostingOwnerV2); err != nil {
		t.Fatalf("V2 terminal leg: %v", err)
	}
	// Phase 18 blocker 1: prove component valuation through the production
	// resolver before posting. The production JoinRatingResolver with the
	// legacy-tag tariff must rate from V2 quantities (valuation non-empty),
	// never from the divergent V1 1M/1M scalar tokens above.
	aware, ok := prod.BillingCallRatingResolver.(billing.OwnerAwareCallRatingResolver)
	if !ok {
		t.Fatalf("production resolver must implement OwnerAwareCallRatingResolver")
	}
	completeForProof, err := billing.JoinCompleteCall(closure, []billing.CallLegUsageRecord{leg})
	if err != nil {
		// Join requires sealed replay identity; the terminal rows above are
		// unsealed pre-store copies. Rate through the same production input
		// the worker will claim (unsealed closure/legs seal inside RateCall).
		completeForProof = billing.CompleteCall{Closure: closure, Legs: []billing.CallLegUsageRecord{leg}}
	}
	proofExp, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatalf("proof exposure: %v", err)
	}
	proof, err := aware.ResolveCallRatingForOwner(ctx, completeForProof, proofExp, billing.PostingOwnerV2)
	if err != nil {
		t.Fatalf("production V2 component rating: %v", err)
	}
	if proof.CustomerValuation.ID == "" {
		t.Fatalf("production V2 result must carry component valuation, got %+v", proof)
	}
	if proof.CustomerCharge.Nano == 303 {
		t.Fatalf("production V2 charge = 303 scalar from V1 1M/1M; must rate from V2 5/2 quantities")
	}
	// Claim/post exactly once with V2 pins/tokens through production workers,
	// using the production customer resolver (not a scalar stub) so the
	// posted money is the component valuation above.
	provWorker, err := billing.NewCallProviderCostWorkerWithCutover(store, store, f3rbProviderStub{}, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := provWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("provider worker: %v", err)
	}
	custWorker, err := billing.NewCallPostUsageWorkerWithCutover(store, store, prod.BillingCallRatingResolver, store, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := custWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("customer worker: %v", err)
	}
	acct, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.BalanceNano != 90000-proof.CustomerCharge.Nano {
		t.Fatalf("balance = %d, want %d (component charge %d, not stub/scalar)", acct.BalanceNano, 90000-proof.CustomerCharge.Nano, proof.CustomerCharge.Nano)
	}
	txs, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("journals = %d, want 2 (customer+provider exactly once)", len(txs))
	}
	afterPin, err := store.GetPostingPin(ctx, billing.PostingOperationCustomerSettlement, opKey)
	if err != nil {
		t.Fatal(err)
	}
	if afterPin.Owner != billing.PostingOwnerV2 || !afterPin.IsCompleted() {
		t.Fatalf("customer pin must be V2 completed, got %#v", afterPin)
	}
	if expAfter, err := store.GetCallExposure(ctx, callID); err != nil {
		t.Fatal(err)
	} else if expAfter.IsOpen() {
		t.Fatalf("V2 exposure must close")
	}
}

// f3rbV2Observation builds one canonical V2 B-leg quantity observation for
// the legacy-mapped component path: input/output token measures under the
// legacy tariff's component keys. Quantities (5/2) diverge from the V1
// scalar tokens (1M/1M) on the same leg so the test proves component
// valuation rather than merely owner token.
func f3rbV2Observation(t *testing.T, storeID string, callID billing.BillingCallID, bLegID string) metering.Observation {
	t.Helper()
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	inputVal, err := metering.ParseDecimal("5")
	if err != nil {
		t.Fatal(err)
	}
	outputVal, err := metering.ParseDecimal("2")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 500000000).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "f3rb-v2", SourceEventKey: "f3rb-v2-source", Revision: 1, StreamID: "f3rb-v2-stream", Sequence: 1,
		Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTransport, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: storeID, ALegID: "a-f3rb", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: storeID, CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-f3rb", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "f3rb:v2:v1",
		Measures: []metering.Measure{
			{Key: inputKey, Value: &inputVal, Quality: metering.QualityObserved},
			{Key: outputKey, Value: &outputVal, Quality: metering.QualityObserved},
		},
	}
}

func TestF3RuntimeBundleExplicitV2RejectedPreactive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := f3rbNewStore(t, "f3rb-pre")
	prod := f3rbCompose(t, store)
	accountID := "acct-f3rb-pre"
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 10000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	in := f3rbAdmissionInput(callID.String(), accountID)
	// Explicit V2 via adapter is rejected pre-active (v1_active, no marker).
	type v2Admitter interface {
		AdmitV2(context.Context, coreRuntime.BillingExposureAdmissionInput) (billing.CallExposure, error)
	}
	adapter, ok := prod.BillingExposureAdmission.(v2Admitter)
	if !ok {
		t.Fatalf("production adapter must expose explicit AdmitV2")
	}
	if _, err := adapter.AdmitV2(ctx, in); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("preactive AdmitV2 err = %v, want V2NotAuthorized", err)
	}
	// Explicit store V2 likewise rejected.
	closure := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID,
		ALegID: "a-f3rb", SessionID: "sess-f3rb",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome:            billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "pricing", Version: "v7"},
		ChargePolicyRef:    billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs:    []string{"b-x"},
	}
	if _, err := store.AdmitExposureWithOwner(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(),
		Max:        billing.Money{Nano: 50, Currency: "USD"},
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}, billing.PostingOwnerV2); !errors.Is(err, billing.ErrCutoverV2NotAuthorized) {
		t.Fatalf("preactive store V2 err = %v, want V2NotAuthorized", err)
	}
}
