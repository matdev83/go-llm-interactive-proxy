package billing

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// LegacyScalarRaterID identifies the compatibility adapter for the historic
// per-million-token PricingSnapshot. It is deliberately named in the
// material so replay cannot confuse a scalar card with a generic tariff.
const LegacyScalarRaterID = "legacy_scalar_v1"

// LegacyScalarSemantics is the published meaning of the old integer fields:
// each token dimension is multiplied by its per-million nano rate and rounded
// toward zero at the line boundary, matching the pre-V2 rating path.
const LegacyScalarSemantics = economics.LegacyScalarSemanticsV1

// PricingSnapshotToTariff adapts an existing scalar customer pricing card into
// immutable component rules. No provider lookup or external pricing source is
// involved; the input card remains the sole source of truth.
func PricingSnapshotToTariff(snapshot PricingSnapshot) (economics.TariffSnapshot, error) {
	if err := snapshot.Validate(snapshot.Currency); err != nil {
		return economics.TariffSnapshot{}, fmt.Errorf("billing: legacy pricing snapshot: %w", err)
	}
	currency, err := economics.NormalizeCurrency(snapshot.Currency)
	if err != nil {
		return economics.TariffSnapshot{}, fmt.Errorf("billing: legacy pricing currency: %w", err)
	}
	ref := economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{
			ID: snapshot.Ref.ID, Version: snapshot.Ref.Version,
			EffectiveAt: snapshot.Ref.EffectiveAt, FetchedAt: snapshot.Ref.FetchedAt,
		},
		RaterID: LegacyScalarRaterID,
	}
	rules := make([]economics.RatingRule, 0, 2+len(snapshot.FixedCharges)+len(snapshot.ResourceCharges))
	if snapshot.InputRatePresent {
		numerator, denominator, err := legacyPerMillionRate(snapshot.InputPerMillionNano)
		if err != nil {
			return economics.TariffSnapshot{}, fmt.Errorf("billing: legacy input rate: %w", err)
		}
		key := metering.ComponentKey{
			Direction: metering.DirectionInput, Component: metering.ComponentInputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		}
		rules = append(rules, economics.RatingRule{
			ID: "legacy.input_token", Kind: economics.RatingRuleLinear, Component: &key,
			Currency: currency, RateNumerator: &numerator, RateDenominator: &denominator, RoundingScope: economics.RoundingScopeLine,
			RoundingPolicy: economics.RoundingTowardZero,
		})
	}
	if snapshot.OutputRatePresent {
		numerator, denominator, err := legacyPerMillionRate(snapshot.OutputPerMillionNano)
		if err != nil {
			return economics.TariffSnapshot{}, fmt.Errorf("billing: legacy output rate: %w", err)
		}
		key := metering.ComponentKey{
			Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken,
			Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		}
		rules = append(rules, economics.RatingRule{
			ID: "legacy.output_token", Kind: economics.RatingRuleLinear, Component: &key,
			Currency: currency, RateNumerator: &numerator, RateDenominator: &denominator, RoundingScope: economics.RoundingScopeLine,
			RoundingPolicy: economics.RoundingTowardZero,
		})
	}
	appendFixed := func(prefix string, components []ChargeComponent) error {
		for i, component := range components {
			if err := component.Amount.Validate(); err != nil {
				return fmt.Errorf("billing: legacy %s[%d] %q: %w", prefix, i, component.Name, err)
			}
			amount, err := legacyNanoAmount(component.Amount.Nano)
			if err != nil {
				return fmt.Errorf("billing: legacy %s[%d] %q: %w", prefix, i, component.Name, err)
			}
			// Keep the old charge name in the rule identity. Duplicate names
			// are still distinct source entries and must not collapse silently.
			id := fmt.Sprintf("legacy.%s.%s.%d", prefix, strings.TrimSpace(component.Name), i)
			if strings.TrimSpace(component.Name) == "" {
				id = fmt.Sprintf("legacy.%s.%d", prefix, i)
			}
			rules = append(rules, economics.RatingRule{
				ID: id, Kind: economics.RatingRuleFixed, Currency: component.Amount.Currency,
				FixedAmount: &amount, FixedScope: economics.FixedFeeScopeCall,
				RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingTowardZero,
			})
		}
		return nil
	}
	if err := appendFixed("fixed", snapshot.FixedCharges); err != nil {
		return economics.TariffSnapshot{}, err
	}
	if err := appendFixed("resource", snapshot.ResourceCharges); err != nil {
		return economics.TariffSnapshot{}, err
	}
	tariff, err := economics.BuildTariffSnapshot(ref, currency, rules)
	if err != nil {
		return economics.TariffSnapshot{}, fmt.Errorf("billing: legacy tariff: %w", err)
	}
	tariff.CatalogVersion = snapshot.Ref.Version
	tariff.LegacySemantics = LegacyScalarSemantics
	// Build once more after adding the catalog metadata so the content hash
	// covers every field that affects replay.
	tariff.Content = economics.SnapshotContentRef{}
	tariff, err = tariff.Canonical()
	if err != nil {
		return economics.TariffSnapshot{}, fmt.Errorf("billing: legacy tariff metadata: %w", err)
	}
	return tariff, nil
}

// LegacyTariffFromPricing is a descriptive compatibility alias.
func LegacyTariffFromPricing(snapshot PricingSnapshot) (economics.TariffSnapshot, error) {
	return PricingSnapshotToTariff(snapshot)
}

func legacyPerMillionRate(rate int64) (metering.Decimal, metering.Decimal, error) {
	if rate < 0 {
		return metering.Decimal{}, metering.Decimal{}, fmt.Errorf("negative per-million rate")
	}
	numerator, err := (metering.Decimal{Coefficient: strconv.FormatInt(rate, 10)}).Normalize()
	if err != nil {
		return metering.Decimal{}, metering.Decimal{}, err
	}
	denominator, err := (metering.Decimal{Coefficient: "1000000000000000"}).Normalize()
	if err != nil {
		return metering.Decimal{}, metering.Decimal{}, err
	}
	return numerator, denominator, nil
}

func legacyNanoAmount(nano int64) (metering.Decimal, error) {
	if nano < 0 {
		return metering.Decimal{}, fmt.Errorf("negative fixed amount")
	}
	return (metering.Decimal{Coefficient: strconv.FormatInt(nano, 10), Scale: metering.LedgerNanoScale}).Normalize()
}
