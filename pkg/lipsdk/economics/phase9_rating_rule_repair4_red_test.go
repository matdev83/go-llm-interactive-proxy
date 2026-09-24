package economics

import (
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair4_LegacyRuleInferenceRejectsConversionAndStrayMaterial(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.v1"}
	for _, tc := range []struct {
		name string
		rule RatingRule
	}{
		{name: "legacy conversion schema", rule: RatingRule{ID: "legacy-conversion", Component: &key, Currency: "USD", ConversionSchema: "image-to-token-v1"}},
		{name: "legacy tier mode without tiers", rule: RatingRule{ID: "legacy-stray-tier-mode", Component: &key, Currency: "USD", UnitPrice: decimalForPhase9Repair4(t, "1"), TierMode: TierAllUnits}},
		{name: "legacy conversion with direct rate", rule: RatingRule{ID: "legacy-conversion-rate", Component: &key, Currency: "USD", UnitPrice: decimalForPhase9Repair4(t, "1"), ConversionSchema: "image-to-token-v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := RatingSnapshotRef{VersionRef: VersionRef{ID: "phase9-repair4", Version: tc.name}, RaterID: "reference"}
			_, err := BuildTariffSnapshot(ref, "USD", []RatingRule{tc.rule})
			if !errors.Is(err, ErrInvalidRatingRule) {
				t.Fatalf("BuildTariffSnapshot error=%v, want %v", err, ErrInvalidRatingRule)
			}
		})
	}
}

func TestPhase9Repair4_ExplicitConversionRejectsContradictoryFields(t *testing.T) {
	t.Parallel()
	key := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "phase9.v1"}
	for _, tc := range []struct {
		name string
		rule RatingRule
	}{
		{name: "direct rate", rule: RatingRule{ID: "conversion-rate", Kind: RatingRuleConversion, Component: &key, Currency: "USD", UnitPrice: decimalForPhase9Repair4(t, "1"), ConversionSchema: "image-to-token-v1"}},
		{name: "tiers", rule: RatingRule{ID: "conversion-tiers", Kind: RatingRuleConversion, Component: &key, Currency: "USD", TierMode: TierAllUnits, Tiers: []RatingTier{{UnitPrice: decimalForPhase9Repair4(t, "1")}}, ConversionSchema: "image-to-token-v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := RatingSnapshotRef{VersionRef: VersionRef{ID: "phase9-repair4-explicit", Version: tc.name}, RaterID: "reference"}
			_, err := BuildTariffSnapshot(ref, "USD", []RatingRule{tc.rule})
			if !errors.Is(err, ErrInvalidRatingRule) {
				t.Fatalf("BuildTariffSnapshot error=%v, want %v", err, ErrInvalidRatingRule)
			}
		})
	}
}

func decimalForPhase9Repair4(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	parsed, err := metering.ParseDecimal(strings.TrimSpace(value))
	if err != nil {
		t.Fatalf("ParseDecimal(%q): %v", value, err)
	}
	return &parsed
}
