package billing

import (
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 18 blocker 1 GREEN: owner-aware customer rating selection.
// V2-owned work uses component/V2 semantics (including explicitly mapped
// legacy tariffs where supported); missing/unsupported V2 evidence fails
// closed before money; scalar survives only as an explicitly authorized V1
// drain/replay path unreachable for V2 owner/token.

func phase18R1Pricing() PricingSnapshot {
	return PricingSnapshot{
		Ref:                  VersionRef{ID: "prices", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []ChargeComponent{{Name: "request", Amount: Money{Nano: 10, Currency: "USD"}}},
	}
}

func phase18R1Policy(pricing PricingSnapshot) ChargePolicy {
	return ChargePolicy{
		Ref:                 VersionRef{ID: "policy", Version: "v2"},
		PricingRef:          pricing.Ref,
		Scope:               ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
}

func phase18R1Call(t *testing.T, pricing PricingSnapshot, policy ChargePolicy, bLeg string) (CallUsageRecord, BillingCallID) {
	t.Helper()
	callID := mustBillingCallID(t)
	call := testCallUsageRecord(callID)
	call.ALegID = "a-1"
	call.ExpectedBLegIDs = []string{bLeg}
	call.CustomerPricingRef = pricing.Ref
	call.ChargePolicyRef = policy.Ref
	return call, callID
}

func phase18R1V2Leg(t *testing.T, callID BillingCallID, bLeg, inputQty, outputQty string) CallLegUsageRecord {
	t.Helper()
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	obs := phase10RetailObservation(t, callID, bLeg, "v2-"+bLeg, metering.OriginLocal, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: inputKey, quantity: inputQty},
		phase10RetailMeasure{key: outputKey, quantity: outputQty},
	)
	leg := testCallLegUsageRecord(callID, bLeg)
	leg.ALegID = "a-1"
	leg.BackendID = "backend-a"
	leg.ModelID = "model-a"
	leg.AttemptSeq = 1
	// Keep divergent V1 scalar tokens to prove the component path rates from
	// canonical V2 quantities, never from InputTokens/OutputTokens.
	leg.Evidence.InputTokens = Quantity{Value: 1_000_000, Present: true}
	leg.Evidence.OutputTokens = Quantity{Value: 1_000_000, Present: true}
	leg.EvidenceVersion = EvidenceFormatVersionV2
	leg.EvidenceProjection = EvidenceProjectionV1
	leg.Observations = []metering.Observation{obs}
	return leg
}

func TestPhase18R1V2LegacyTariffRatesFromV2ComponentQuantities(t *testing.T) {
	t.Parallel()
	pricing := phase18R1Pricing()
	policy := phase18R1Policy(pricing)
	call, callID := phase18R1Call(t, pricing, policy, "b-win")
	leg := phase18R1V2Leg(t, callID, "b-win", "5", "2")
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
		PostingOwner: PostingOwnerV2,
	})
	if err != nil {
		t.Fatalf("V2 legacy-mapped component rating: %v", err)
	}
	if result.CustomerValuation.ID == "" {
		t.Fatalf("V2 result must carry a component valuation, got %+v", result)
	}
	if result.CustomerCharge == (Money{Nano: 310, Currency: "USD"}) {
		t.Fatalf("V2 charge = 310 scalar from V1 1M/1M; must rate from V2 5/2 quantities")
	}
	if err := ValidateCallRatingResultForOwner(result, PostingOwnerV2); err != nil {
		t.Fatalf("V2 valuation boundary: %v", err)
	}
	// Fixed 10 nanos plus tiny V2 usage must be far below the 310 scalar.
	if result.CustomerCharge.Nano >= 310 || result.CustomerCharge.Nano < 10 {
		t.Fatalf("V2 component charge = %+v, want fixed-dominated amount in [10,310)", result.CustomerCharge)
	}
}

func TestPhase18R1V1DrainSettlesScalarExactlyOnce(t *testing.T) {
	t.Parallel()
	pricing := phase18R1Pricing()
	policy := phase18R1Policy(pricing)
	call, callID := phase18R1Call(t, pricing, policy, "b-win")
	leg := testCallLegUsageRecord(callID, "b-win")
	leg.ALegID = "a-1"
	leg.Evidence.InputTokens = Quantity{Value: 1_000_000, Present: true}
	leg.Evidence.OutputTokens = Quantity{Value: 1_000_000, Present: true}
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
		PostingOwner: PostingOwnerV1,
	})
	if err != nil {
		t.Fatalf("V1 drain rating: %v", err)
	}
	if result.CustomerCharge != (Money{Nano: 310, Currency: "USD"}) {
		t.Fatalf("V1 drain charge = %+v, want exact historical 310", result.CustomerCharge)
	}
	if result.CustomerValuation.ID != "" {
		t.Fatalf("V1 drain must not synthesize a component valuation, got %+v", result.CustomerValuation)
	}
	// Replay is deterministic: identical input rates identically.
	again, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
		PostingOwner: PostingOwnerV1,
	})
	if err != nil {
		t.Fatalf("V1 drain replay: %v", err)
	}
	if again.CustomerCharge != result.CustomerCharge || again.Fingerprint != result.Fingerprint {
		t.Fatalf("V1 replay changed outcome: first=%+v second=%+v", result, again)
	}
}

func TestPhase18R1V2MissingEvidenceFailsClosed(t *testing.T) {
	t.Parallel()
	pricing := phase18R1Pricing()
	policy := phase18R1Policy(pricing)
	call, callID := phase18R1Call(t, pricing, policy, "b-win")
	leg := testCallLegUsageRecord(callID, "b-win")
	leg.ALegID = "a-1"
	leg.Evidence.InputTokens = Quantity{Value: 1_000_000, Present: true}
	leg.Evidence.OutputTokens = Quantity{Value: 1_000_000, Present: true}
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
		PostingOwner: PostingOwnerV2,
	})
	if err == nil {
		t.Fatalf("V2 owner with V1-only legs (missing V2 evidence) must fail closed, not scalar")
	}
	if !errors.Is(err, ErrRetailRateIncomplete) {
		t.Fatalf("error = %v, want %v", err, ErrRetailRateIncomplete)
	}
}

func TestPhase18R1DirectV2CannotForceScalarViaTagOrSelector(t *testing.T) {
	t.Parallel()
	pricing := phase18R1Pricing()
	policy := phase18R1Policy(pricing)
	call, callID := phase18R1Call(t, pricing, policy, "b-win")
	// V2 evidence present but the legacy tag must not force scalar: the
	// component path is selected and the result carries a valuation.
	leg := phase18R1V2Leg(t, callID, "b-win", "7", "3")
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatal(err)
	}
	if !isLegacyScalarTariff(tariff) {
		t.Fatalf("fixture tariff must carry the legacy tag, got %+v", tariff.Ref)
	}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
		PostingOwner: PostingOwnerV2,
	})
	if err != nil {
		t.Fatalf("V2 legacy tag must map into component path where supported: %v", err)
	}
	if result.CustomerValuation.ID == "" {
		t.Fatalf("V2 legacy-tag result must carry component valuation, got %+v", result)
	}
	// A scalar result (empty valuation) is rejected for V2 at the posting
	// boundary even if a caller bypassed selection.
	scalar := CallRatingResult{CallID: callID, CustomerCharge: Money{Nano: 310, Currency: "USD"}, Fingerprint: "scalar"}
	if err := ValidateCallRatingResultForOwner(scalar, PostingOwnerV2); err == nil {
		t.Fatalf("V2 posting boundary must reject scalar without valuation")
	}
	if err := ValidateCallRatingResultForOwner(scalar, PostingOwnerV1); err != nil {
		t.Fatalf("V1 drain boundary must accept historical scalar: %v", err)
	}
}

func TestPhase18R1V1OwnerPreservesFrozenChargeDespiteAdditiveV2(t *testing.T) {
	t.Parallel()
	pricing := phase18R1Pricing()
	policy := phase18R1Policy(pricing)
	call, callID := phase18R1Call(t, pricing, policy, "b-win")
	// Same frozen V1 scalar (1M/1M => 310) with divergent additive V2
	// captures. Durable posting ownership, not envelope format, selects the
	// frozen monetary writer: both must settle the historical V1 charge.
	legA := phase18R1V2Leg(t, callID, "b-win", "5", "2")
	legB := phase18R1V2Leg(t, callID, "b-win", "7", "3")
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatal(err)
	}
	// Prove the envelope is V2-format yet V1-owned drain still applies.
	for i, leg := range []CallLegUsageRecord{legA, legB} {
		version, verr := WriterVersionForLeg(leg)
		if verr != nil {
			t.Fatalf("leg %d WriterVersionForLeg: %v", i, verr)
		}
		if version != V2WriterVersion {
			t.Fatalf("leg %d writer = %q, want %q (additive V2 envelope)", i, version, V2WriterVersion)
		}
		if !leg.Evidence.InputTokens.Present || leg.Evidence.InputTokens.Value != 1_000_000 {
			t.Fatalf("leg %d original V1 scalar evidence must be preserved, got %+v", i, leg.Evidence.InputTokens)
		}
		if len(leg.Observations) == 0 {
			t.Fatalf("leg %d additive V2 observations must be preserved, not stripped", i)
		}
	}
	rate := func(leg CallLegUsageRecord) CallRatingResult {
		t.Helper()
		result, rerr := RateCall(CallRatingInput{
			Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
			CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
			PostingOwner: PostingOwnerV1,
		})
		if rerr != nil {
			t.Fatalf("V1 drain with additive V2 must preserve frozen charge: %v", rerr)
		}
		return result
	}
	first := rate(legA)
	second := rate(legB)
	if first.CustomerCharge != (Money{Nano: 310, Currency: "USD"}) {
		t.Fatalf("V1 frozen charge = %+v, want exact historical 310", first.CustomerCharge)
	}
	if second.CustomerCharge != first.CustomerCharge {
		t.Fatalf("divergent additive V2 changed V1 charge: first=%+v second=%+v", first.CustomerCharge, second.CustomerCharge)
	}
	if first.CustomerValuation.ID != "" || second.CustomerValuation.ID != "" {
		t.Fatalf("V1 drain must not synthesize a component valuation, got %+v / %+v", first.CustomerValuation, second.CustomerValuation)
	}
	if err := ValidateCallRatingResultForOwner(first, PostingOwnerV1); err != nil {
		t.Fatalf("V1 drain boundary must accept frozen scalar with additive V2: %v", err)
	}
	// Replay of the same durable V1+V2 leg is deterministic.
	again := rate(legA)
	if again.CustomerCharge != first.CustomerCharge || again.Fingerprint != first.Fingerprint {
		t.Fatalf("V1 replay changed outcome: first=%+v again=%+v", first, again)
	}
}

func TestPhase18R1UnknownRatingOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	pricing := phase18R1Pricing()
	policy := phase18R1Policy(pricing)
	call, callID := phase18R1Call(t, pricing, policy, "b-win")
	leg := testCallLegUsageRecord(callID, "b-win")
	leg.ALegID = "a-1"
	_, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy,
		PostingOwner: "bogus",
	})
	if err == nil {
		t.Fatalf("unknown rating owner must fail closed")
	}
	_ = time.Now
}
