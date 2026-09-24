package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 18 final owner remediation (R2): generic worker/result fence.
//
// Production/cutover V2 requires OwnerAwareCallRatingResolver; an old
// owner-unaware resolver (including a decorator exposing only that port)
// fails closed for V2 before any money. Every resolved result is
// independently validated for its owner via ValidateCallRatingResultForOwner:
// V2 requires a component CustomerValuation or explicit pass-through, while
// V1 drain and legacy empty owners preserve historical scalar replay.

type ownerFinalOldScalarResolver struct {
	charge int64
	fp     string
	calls  int
}

func (s *ownerFinalOldScalarResolver) ResolveCallRating(_ context.Context, complete CompleteCall, _ CallExposure) (CallRatingResult, error) {
	s.calls++
	return CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp}, nil
}

type ownerFinalComponentResolver struct {
	t      *testing.T
	charge int64
	calls  int
}

// ownerFinalBoundValuation builds a complete customer-policy component
// valuation bound to the actual settled closure and charge. Settlement
// identity (account/call, scope, payer, policy/tariff snapshots, currency,
// amount) derives from the live closure and charge; only snapshot content
// digests use deterministic fixture hashing in the phase10 manner. The
// caller must use the valuation fingerprint as the result fingerprint.
func ownerFinalBoundValuation(t *testing.T, closure CallUsageRecord, charge Money) economics.Valuation {
	t.Helper()
	const storeID = "test"
	callID := closure.CallID
	ref := metering.ObservationRef{StoreID: storeID, ObservationID: "observation-" + callID.String(), Revision: 1, PayloadHash: "payload-" + callID.String()}
	inputHash, err := economics.CanonicalInputSetHash(economics.BasisCustomerPolicy, []metering.ObservationRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	tariffID, tariffVersion := closure.CustomerPricingRef.ID, closure.CustomerPricingRef.Version
	policyID, policyVersion := closure.ChargePolicyRef.ID, closure.ChargePolicyRef.Version
	hash := func(parts ...string) string {
		sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		return hex.EncodeToString(sum[:])
	}
	decimal := func(nano int64) metering.Decimal {
		value, err := metering.ParseDecimal(fmt.Sprintf("%d.%09d", nano/1_000_000_000, nano%1_000_000_000))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	amount := decimal(charge.Nano)
	unitPrice := decimal(charge.Nano)
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
	totalAmount := decimal(charge.Nano)
	v := economics.Valuation{
		ID: "valuation-" + callID.String(), Version: economics.ValuationVersionV2, Perspective: metering.PerspectiveCustomer, Basis: economics.BasisCustomerPolicy,
		Subject: metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: storeID, BillingCallID: callID.String()}, Scope: "call:" + callID.String(),
		InputObservations: []metering.ObservationRef{ref}, InputSetHash: inputHash,
		Rater:                economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: tariffID, Version: tariffVersion}, RaterID: "reference"},
		RaterContent:         &economics.SnapshotContentRef{ContentRef: "rater://" + tariffID + "/" + tariffVersion, ContentHash: hash("tariff", tariffID+"/"+tariffVersion)},
		Tariff:               economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: tariffID, Version: tariffVersion}, RaterID: "reference"},
		TariffContent:        &economics.SnapshotContentRef{ContentRef: "tariff://" + tariffID + "/" + tariffVersion, ContentHash: hash("tariff", tariffID+"/"+tariffVersion)},
		Policy:               economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: policyID, Version: policyVersion}, PolicyID: policyID},
		PolicyContent:        &economics.SnapshotContentRef{ContentRef: "policy://" + policyID + "/" + policyVersion, ContentHash: hash("policy", policyID+"/"+policyVersion)},
		QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "qualifier://" + tariffID + "/" + tariffVersion + "/" + policyID + "/" + policyVersion, ContentHash: hash("qualifier", tariffID+"/"+tariffVersion+"/"+policyID+"/"+policyVersion)},
		Payer:                metering.PaymentParty{Kind: metering.PaymentPartyCustomer, ID: closure.AccountID},
		Lines:                []economics.LineItem{line}, Totals: []economics.CurrencyTotal{{Currency: charge.Currency, Amount: &totalAmount, RoundedAmount: rounded}},
		Completeness: economics.CompletenessComplete, CreatedAt: time.Unix(101, 0).UTC(),
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("bound fixture valuation must validate: %v", err)
	}
	return v
}

// ownerFinalBoundResult binds one stub charge to the actual complete closure:
// the valuation carries the closure's account/call/policy/tariff identity
// and the charge currency/amount, and the result fingerprint is the
// valuation fingerprint so result identity cannot drift from rated content.
func ownerFinalBoundResult(t *testing.T, closure CallUsageRecord, chargeNano int64) CallRatingResult {
	t.Helper()
	charge := Money{Nano: chargeNano, Currency: "USD"}
	valuation := ownerFinalBoundValuation(t, closure, charge)
	fp := valuation.Fingerprint()
	if fp == "" {
		t.Fatalf("bound fixture valuation fingerprint is empty")
	}
	return CallRatingResult{CallID: closure.CallID, CustomerCharge: charge, Fingerprint: fp, CustomerValuation: valuation}
}

func (s *ownerFinalComponentResolver) ResolveCallRating(_ context.Context, complete CompleteCall, _ CallExposure) (CallRatingResult, error) {
	s.calls++
	return ownerFinalBoundResult(s.t, complete.Closure, s.charge), nil
}

func (s *ownerFinalComponentResolver) ResolveCallRatingForOwner(_ context.Context, complete CompleteCall, _ CallExposure, owner string) (CallRatingResult, error) {
	s.calls++
	result := ownerFinalBoundResult(s.t, complete.Closure, s.charge)
	if owner != PostingOwnerV2 {
		result.CustomerValuation = economics.Valuation{}
	}
	return result, nil
}

type ownerFinalScalarOwnerAwareResolver struct {
	charge int64
	fp     string
}

func (s ownerFinalScalarOwnerAwareResolver) ResolveCallRating(_ context.Context, complete CompleteCall, _ CallExposure) (CallRatingResult, error) {
	return CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp}, nil
}

func (s ownerFinalScalarOwnerAwareResolver) ResolveCallRatingForOwner(_ context.Context, complete CompleteCall, _ CallExposure, _ string) (CallRatingResult, error) {
	return CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: s.charge, Currency: "USD"}, Fingerprint: s.fp}, nil
}

// ownerFinalDecoratedResolver wraps an owner-aware inner but exposes only
// the legacy port, proving a decorator cannot smuggle V2 scalar past the
// generic fence.
type ownerFinalDecoratedResolver struct {
	inner *ownerFinalComponentResolver
}

func (s ownerFinalDecoratedResolver) ResolveCallRating(ctx context.Context, complete CompleteCall, exposure CallExposure) (CallRatingResult, error) {
	// Deliberately returns scalar without valuation even though the inner
	// could produce a component: the decorator hides the owner-aware port.
	return CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: 120, Currency: "USD"}, Fingerprint: "decorated-scalar"}, nil
}

func ownerFinalComplete(t *testing.T) (CompleteCall, CallExposure) {
	t.Helper()
	callID := mustBillingCallID(t)
	closure := testCallUsageRecord(callID)
	closure.ALegID = "a-1"
	closure.ExpectedBLegIDs = []string{"b-win"}
	leg := testCallLegUsageRecord(callID, "b-win")
	leg.ALegID = "a-1"
	leg.AttemptSeq = 1
	complete := CompleteCall{Closure: closure, Legs: []CallLegUsageRecord{leg}}
	exposure := CallExposure{
		AccountID: closure.AccountID, CallID: callID.String(),
		Max: Money{Nano: 1000, Currency: "USD"}, Status: ExposureOpen, CreatedAt: time.Unix(1, 0).UTC(),
		PricingRef: closure.CustomerPricingRef, ChargePolicyRef: closure.ChargePolicyRef,
	}
	return complete, exposure
}

func TestOwnerFinalV2RequiresOwnerAwareResolver(t *testing.T) {
	t.Parallel()
	complete, exposure := ownerFinalComplete(t)
	for _, tc := range []struct {
		name     string
		resolver CallRatingResolver
	}{
		{"old-scalar", &ownerFinalOldScalarResolver{charge: 120, fp: "old-scalar-fp"}},
		{"decorated-hides-owner-aware", ownerFinalDecoratedResolver{inner: &ownerFinalComponentResolver{t: t, charge: 120}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			usage := &fakeCallUsageStore{}
			settlement := &countingSettlementStore{}
			worker, err := NewCallPostUsageWorker(usage, settlement, tc.resolver, 8)
			if err != nil {
				t.Fatal(err)
			}
			_, err = worker.resolveCallRatingWithOwner(context.Background(), complete, exposure, PostingOwnerV2)
			if err == nil {
				t.Fatalf("V2 with legacy/decorated resolver must fail closed, not post scalar")
			}
			if !errors.Is(err, ErrPostingOwnershipInvalid) && !errors.Is(err, ErrRetailRateIncomplete) {
				t.Fatalf("error = %v, want ownership/retail fence", err)
			}
			if settlement.calls != 0 {
				t.Fatalf("settlement calls = %d, want 0 (fail before Apply)", settlement.calls)
			}
		})
	}
}

func TestOwnerFinalV2ScalarOwnerAwareFailsResultFence(t *testing.T) {
	t.Parallel()
	complete, exposure := ownerFinalComplete(t)
	worker, err := NewCallPostUsageWorker(&fakeCallUsageStore{}, &countingSettlementStore{}, ownerFinalScalarOwnerAwareResolver{charge: 120, fp: "scalar-aware-fp"}, 8)
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.resolveCallRatingWithOwner(context.Background(), complete, exposure, PostingOwnerV2)
	if err == nil {
		t.Fatalf("V2 scalar result from owner-aware resolver must fail the result fence")
	}
	if !errors.Is(err, ErrRetailRateIncomplete) {
		t.Fatalf("error = %v, want %v", err, ErrRetailRateIncomplete)
	}
}

func TestOwnerFinalV2ComponentAndPassThroughSucceed(t *testing.T) {
	t.Parallel()
	complete, exposure := ownerFinalComplete(t)
	worker, err := NewCallPostUsageWorker(&fakeCallUsageStore{}, &countingSettlementStore{}, &ownerFinalComponentResolver{t: t, charge: 120}, 8)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.resolveCallRatingWithOwner(context.Background(), complete, exposure, PostingOwnerV2)
	if err != nil {
		t.Fatalf("V2 component valuation must succeed: %v", err)
	}
	if result.CustomerValuation.ID == "" {
		t.Fatalf("V2 component result must carry valuation, got %+v", result)
	}
	// Explicit pass-through is the only other valid V2 outcome, under its
	// complete contract (provisional posting requires late-adjustment
	// permission in the frozen policy).
	passThrough := CallRatingResult{
		CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: 50, Currency: "USD"}, Fingerprint: "pass-through-fp",
		CostPassThrough: &CostPassThroughSettlement{
			PolicyRef: complete.Closure.ChargePolicyRef,
			Policy: CostPassThroughPolicy{
				MissingCost: CostPassThroughMissingCostProvisional,
				SafeBound:   &Money{Nano: 100, Currency: "USD"}, AllowLateAdjustment: true,
			},
			Status:    CostPassThroughSettlementProvisional,
			SafeBound: Money{Nano: 100, Currency: "USD"}, PostedAmount: Money{Nano: 50, Currency: "USD"},
		},
	}
	if err := ValidateCallRatingResultForOwner(passThrough, PostingOwnerV2); err != nil {
		t.Fatalf("V2 explicit pass-through must validate: %v", err)
	}
	scalar := CallRatingResult{CallID: complete.Closure.CallID, CustomerCharge: Money{Nano: 120, Currency: "USD"}, Fingerprint: "scalar"}
	if err := ValidateCallRatingResultForOwner(scalar, PostingOwnerV2); !errors.Is(err, ErrRetailRateIncomplete) {
		t.Fatalf("V2 scalar must fail validation, got %v", err)
	}
}

func TestOwnerFinalV1DrainWithLegacyResolverSucceeds(t *testing.T) {
	t.Parallel()
	complete, exposure := ownerFinalComplete(t)
	legacy := &ownerFinalOldScalarResolver{charge: 120, fp: "v1-legacy-fp"}
	worker, err := NewCallPostUsageWorker(&fakeCallUsageStore{}, &countingSettlementStore{}, legacy, 8)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.resolveCallRatingWithOwner(context.Background(), complete, exposure, PostingOwnerV1)
	if err != nil {
		t.Fatalf("V1 drain with legacy resolver must succeed: %v", err)
	}
	if result.CustomerCharge.Nano != 120 {
		t.Fatalf("V1 drain charge = %+v, want legacy 120", result.CustomerCharge)
	}
	// V2 missing evidence via owner-aware selection fails closed (no scalar
	// fallback); covered at rating level by TestPhase18R1V2MissingEvidenceFailsClosed
	// and at the result fence here.
	if err := ValidateCallRatingResultForOwner(CallRatingResult{CallID: complete.Closure.CallID}, PostingOwnerV2); !errors.Is(err, ErrRetailRateIncomplete) {
		t.Fatalf("V2 missing valuation must fail closed")
	}
}

type countingSettlementStore struct {
	calls int
	err   error
}

func (s *countingSettlementStore) ApplyCallBillingResult(context.Context, ApplyCallBillingInput) (CallSettlement, error) {
	s.calls++
	return CallSettlement{}, s.err
}
