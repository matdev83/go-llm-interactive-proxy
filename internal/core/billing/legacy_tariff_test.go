package billing

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPricingSnapshotToTariffPreservesNamedLegacySemantics(t *testing.T) {
	t.Parallel()
	pricing := PricingSnapshot{
		Ref:                 VersionRef{ID: "legacy-pricing", Version: "v3", EffectiveAt: time.Unix(1, 0), FetchedAt: time.Unix(2, 0)},
		Currency:            "USD",
		InputPerMillionNano: 100, OutputPerMillionNano: 200,
		InputRatePresent: true, OutputRatePresent: true,
		FixedCharges: []ChargeComponent{{Name: "request", Amount: Money{Nano: 3, Currency: "USD"}}},
	}
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("PricingSnapshotToTariff: %v", err)
	}
	if tariff.LegacySemantics != LegacyScalarSemantics || tariff.Ref.RaterID != LegacyScalarRaterID {
		t.Fatalf("legacy material=%+v", tariff)
	}
	if len(tariff.Rules) != 3 || tariff.Content.ContentHash != tariff.ContentHash() {
		t.Fatalf("rules/content=%d/%+v", len(tariff.Rules), tariff.Content)
	}
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "legacy-input", metering.OriginLocal, inputKey, "1000000"),
		phase9Observation(t, "legacy-output", metering.OriginLocal, outputKey, "2000000"),
	}, tariff)
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	valuation, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("legacy Rate: %v", err)
	}
	if valuation.Totals[0].RoundedAmount.NanoUnits != 503 {
		t.Fatalf("rounded total=%d, want 503 legacy nanos", valuation.Totals[0].RoundedAmount.NanoUnits)
	}
	// Publication refresh metadata does not alter accepted rule content.
	pricing.Ref.EffectiveAt = time.Unix(99, 0)
	pricing.Ref.FetchedAt = time.Unix(100, 0)
	replay, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("legacy replay: %v", err)
	}
	if replay.ContentHash() != tariff.ContentHash() {
		t.Fatalf("timestamp refresh changed content hash: %s/%s", replay.ContentHash(), tariff.ContentHash())
	}
}

func TestRateCallLegacyScalarTariffIgnoresV2BoundaryQuantityEvidence(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	pricing := PricingSnapshot{
		Ref:                  VersionRef{ID: "prices", Version: "v1"},
		Currency:             "USD",
		InputPerMillionNano:  100,
		OutputPerMillionNano: 200,
		InputRatePresent:     true,
		OutputRatePresent:    true,
		FixedCharges:         []ChargeComponent{{Name: "request", Amount: Money{Nano: 10, Currency: "USD"}}},
	}
	policy := ChargePolicy{
		Ref:                 VersionRef{ID: "policy", Version: "v2"},
		PricingRef:          pricing.Ref,
		Scope:               ChargeSurfacedTurn,
		IncludeInputTokens:  true,
		IncludeOutputTokens: true,
		IncludeFixedCharges: true,
	}
	call := testCallUsageRecord(callID)
	call.ALegID = "a-1"
	call.ExpectedBLegIDs = []string{"b-win"}
	leg := testCallLegUsageRecord(callID, "b-win")
	leg.ALegID = "a-1"
	leg.Evidence.InputTokens = Quantity{Value: 1_000_000, Present: true}
	leg.Evidence.OutputTokens = Quantity{Value: 1_000_000, Present: true}
	inputKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	cacheKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	local := phase10RetailObservation(t, callID, "b-win", "local-boundary", metering.OriginLocal, metering.BoundaryBackendEgress,
		phase10RetailMeasure{key: inputKey, quantity: "1000000"},
		phase10RetailMeasure{key: outputKey, quantity: "1000000"},
	)
	local.Measures = append(local.Measures, metering.Measure{Key: cacheKey, Quality: metering.QualityUnavailable, Reason: "not provided"})
	leg.EvidenceVersion = EvidenceFormatVersionV2
	leg.EvidenceProjection = EvidenceProjectionV1
	leg.Observations = []metering.Observation{local}
	tariff, err := PricingSnapshotToTariff(pricing)
	if err != nil {
		t.Fatalf("PricingSnapshotToTariff: %v", err)
	}
	result, err := RateCall(CallRatingInput{
		Call: call, Legs: []CallLegUsageRecord{leg}, MaxCustomerCharge: Money{Nano: 1_000, Currency: "USD"},
		CustomerPricing: pricing, CustomerPolicy: policy, CustomerTariff: tariff,
	})
	if err != nil {
		t.Fatalf("legacy scalar rating must ignore V2 boundary quantities: %v", err)
	}
	if result.CustomerCharge != (Money{Nano: 310, Currency: "USD"}) {
		t.Fatalf("customer charge = %+v, want 310 USD from V1 scalar evidence", result.CustomerCharge)
	}
}
