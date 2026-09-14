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
