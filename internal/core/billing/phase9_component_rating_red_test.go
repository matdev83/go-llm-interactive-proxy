package billing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9ReferenceRater_AsymmetricMediaAndFixedFee(t *testing.T) {
	t.Parallel()
	imageIn := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	imageOut := phase9Key(metering.DirectionOutput, metering.ComponentImage, metering.UnitImage)
	audioIn := phase9Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	audioOut := phase9Key(metering.DirectionOutput, metering.ComponentAudio, metering.UnitSecond)
	tariff := phase9Tariff(t, []economics.RatingRule{
		phase9LinearRule("image-in", imageIn, "2"),
		phase9LinearRule("image-out", imageOut, "3"),
		phase9LinearRule("audio-in", audioIn, "0.5"),
		phase9LinearRule("audio-out", audioOut, "1"),
		phase9FixedRule("request-fee", economics.FixedFeeScopeCall, "4"),
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "image-in", metering.OriginLocal, imageIn, "2"),
		phase9Observation(t, "image-out", metering.OriginLocal, imageOut, "1"),
		phase9Observation(t, "audio-in", metering.OriginLocal, audioIn, "10"),
		phase9Observation(t, "audio-out", metering.OriginLocal, audioOut, "3"),
	}, tariff)
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if got.Completeness != economics.CompletenessComplete {
		t.Fatalf("completeness=%q", got.Completeness)
	}
	if len(got.Lines) != 5 {
		t.Fatalf("lines=%d, want five independent media/fee lines", len(got.Lines))
	}
	if got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "19/0" {
		t.Fatalf("total=%v, want 19/0", got.Totals)
	}
}

func TestPhase9ReferenceRater_AllNativeNonTokenDirectionsRemainDistinct(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		id, component, unit, price string
		direction                  metering.FlowDirection
	}{
		{id: "image-in", component: metering.ComponentImage, unit: metering.UnitImage, price: "1", direction: metering.DirectionInput},
		{id: "image-out", component: metering.ComponentImage, unit: metering.UnitImage, price: "2", direction: metering.DirectionOutput},
		{id: "audio-in", component: metering.ComponentAudio, unit: metering.UnitSecond, price: "3", direction: metering.DirectionInput},
		{id: "audio-out", component: metering.ComponentAudio, unit: metering.UnitSecond, price: "4", direction: metering.DirectionOutput},
		{id: "video-in", component: metering.ComponentVideo, unit: metering.UnitSecond, price: "5", direction: metering.DirectionInput},
		{id: "video-out", component: metering.ComponentVideo, unit: metering.UnitFrame, price: "6", direction: metering.DirectionOutput},
		{id: "document-page", component: metering.ComponentDocument, unit: metering.UnitPage, price: "7", direction: metering.DirectionInput},
		{id: "request", component: metering.ComponentRequest, unit: metering.UnitCount, price: "8", direction: metering.DirectionNone},
		{id: "time", component: "time", unit: metering.UnitSecond, price: "9", direction: metering.DirectionNone},
		{id: "storage", component: metering.ComponentStorage, unit: metering.UnitByteSecond, price: "10", direction: metering.DirectionNone},
		{id: "credit", component: metering.ComponentCredit, unit: metering.UnitCredit, price: "11", direction: metering.DirectionNone},
		{id: "submission", component: "submission", unit: metering.UnitCount, price: "12", direction: metering.DirectionNone},
	}
	rules := make([]economics.RatingRule, 0, len(fixtures)+1)
	observations := make([]metering.Observation, 0, len(fixtures))
	for _, fixture := range fixtures {
		key := phase9Key(fixture.direction, fixture.component, fixture.unit)
		if fixture.component == metering.ComponentRequest || fixture.component == metering.ComponentStorage || fixture.component == metering.ComponentCredit || fixture.component == "submission" || fixture.component == "time" {
			key = metering.ComponentKey{Direction: metering.DirectionNone, Component: fixture.component, Unit: fixture.unit, SchemaID: "phase9.synthetic.v1"}
		}
		rules = append(rules, phase9LinearRule(fixture.id, key, fixture.price))
		observations = append(observations, phase9Observation(t, fixture.id, metering.OriginLocal, key, "1"))
	}
	rules = append(rules, phase9FixedRule("image-generation", economics.FixedFeeScopeCall, "13"))
	tariff := phase9Tariff(t, rules)
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, observations, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if len(got.Lines) != len(fixtures)+1 {
		t.Fatalf("lines=%d, want %d native lines plus fixed fee", len(got.Lines), len(fixtures)+1)
	}
	seen := make(map[string]struct{}, len(fixtures))
	for _, line := range got.Lines {
		if line.Component != nil {
			seen[line.Component.CanonicalKey()] = struct{}{}
		}
	}
	if len(seen) != len(fixtures) {
		t.Fatalf("component identities collapsed: got %d want %d", len(seen), len(fixtures))
	}
}

func TestPhase9ReferenceRater_ExactRulesAndInformationalTotals(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, "fractional", metering.UnitSecond)
	blockKey := phase9Key(metering.DirectionOutput, "block", metering.UnitCount)
	tierKey := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	infoKey := phase9Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	numerator := phase9Decimal("1")
	denominator := phase9Decimal("3")
	tariff := phase9Tariff(t, []economics.RatingRule{
		{ID: "fractional", Component: &key, Currency: "USD", RateNumerator: numerator, RateDenominator: denominator},
		{ID: "block-minimum", Component: &blockKey, Currency: "USD", UnitPrice: phase9Decimal("2"), BlockSize: phase9Decimal("3"), MinimumAmount: phase9Decimal("10")},
		{ID: "tier", Component: &tierKey, Currency: "USD", TierMode: economics.TierGraduated, Tiers: []economics.RatingTier{{UpTo: phase9Decimal("2"), UnitPrice: phase9Decimal("1")}, {UnitPrice: phase9Decimal("2")}}},
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "fractional", metering.OriginLocal, key, "1"),
		phase9Observation(t, "block", metering.OriginLocal, blockKey, "4"),
		phase9Observation(t, "tier", metering.OriginLocal, tierKey, "3"),
		phase9Observation(t, "informational-total", metering.OriginLocal, infoKey, "999"),
	}, tariff)
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if len(got.Lines) != 3 {
		t.Fatalf("lines=%d, want informational total omitted", len(got.Lines))
	}
	var fractional, block, tier economics.LineItem
	for _, line := range got.Lines {
		switch line.RuleID {
		case "fractional":
			fractional = line
		case "block-minimum":
			block = line
		case "tier":
			tier = line
		}
	}
	if fractional.Amount != nil || fractional.AmountNumerator != "1" || fractional.AmountDenominator != "3" {
		t.Fatalf("fractional amount=%+v, want exact 1/3", fractional)
	}
	if block.Amount == nil || block.Amount.CanonicalString() != "12/0" {
		t.Fatalf("block amount=%v, want 12/0", block.Amount)
	}
	if tier.Amount == nil || tier.Amount.CanonicalString() != "4/0" {
		t.Fatalf("graduated amount=%v, want 4/0", tier.Amount)
	}
}

func TestPhase9ReferenceRater_MinimumOnFreeRateIsNotExplicitFree(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{{
		ID: "minimum-free", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("0"), MinimumAmount: phase9Decimal("5"),
	}})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "minimum-free", metering.OriginLocal, key, "1"),
	}, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if got.Lines[0].Status != economics.RatingLineRated {
		t.Fatalf("status=%q, want rated for a positive minimum", got.Lines[0].Status)
	}
	if got.Lines[0].Amount == nil || got.Lines[0].Amount.CanonicalString() != "5/0" {
		t.Fatalf("amount=%v, want 5/0", got.Lines[0].Amount)
	}
}

func TestPhase9ReferenceRater_AllDeclaredFixedFeesApplyOnce(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{
		phase9LinearRule("image", key, "1"),
		phase9FixedRule("request-fee", economics.FixedFeeScopeCall, "2"),
		phase9FixedRule("image-generation-fee", economics.FixedFeeScopeCall, "3"),
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "image", metering.OriginLocal, key, "1"),
	}, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if len(got.Lines) != 3 || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "6/0" {
		t.Fatalf("lines=%d totals=%v, want image plus both fixed fees once", len(got.Lines), got.Totals)
	}
}

func TestPhase9ReferenceRater_ReasoningOutputIsMeaningfulWhenUnpriced(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionOutput, metering.ComponentReasoningOutputToken, metering.UnitToken)
	tariff := phase9Tariff(t, nil)
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	_, err = rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "reasoning", metering.OriginLocal, key, "3"),
	}, tariff))
	if !errors.Is(err, ErrRateMissing) {
		t.Fatalf("unpriced reasoning output error=%v, want %v", err, ErrRateMissing)
	}
}

func TestPhase9ReferenceRater_PeriodAndOverlapFailClosed(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	periodTariff := phase9Tariff(t, []economics.RatingRule{{
		ID: "period", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), SelectionScope: economics.SelectionPeriod,
	}})
	rater, err := NewReferenceRater(periodTariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{phase9Observation(t, "period", metering.OriginLocal, key, "1")}, periodTariff)
	if _, err := rater.Rate(context.Background(), input); !errors.Is(err, ErrPeriodScopeRequired) {
		t.Fatalf("unscoped period error=%v, want %v", err, ErrPeriodScopeRequired)
	}
	input.Scope = "period:2026-09"
	if _, err := rater.Rate(context.Background(), input); err != nil {
		t.Fatalf("period-scoped rating: %v", err)
	}
	overlap := phase9Tariff(t, []economics.RatingRule{
		{ID: "a", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}},
		{ID: "b", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("2"), Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}},
	})
	if _, err := NewReferenceRater(overlap); !errors.Is(err, economics.ErrRatingRuleOverlap) {
		t.Fatalf("overlap error=%v, want %v", err, economics.ErrRatingRuleOverlap)
	}
}

func TestPhase9ReferenceRater_EAndQRemainIndependentWhenPExists(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "2")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	local := phase9Observation(t, "local", metering.OriginLocal, key, "2")
	provider := phase9Observation(t, "provider", metering.OriginProvider, key, "3")
	provider.Charges = []metering.ReportedCharge{{ChargeItemID: "p", Component: &key, Amount: phase9Decimal("9"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	base := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{local, provider}, tariff)
	base.Basis = economics.BasisLocalExpected
	all, err := RateIndependentValuations(context.Background(), rater, base)
	if err != nil {
		t.Fatalf("RateIndependentValuations: %v", err)
	}
	if all.Expected == nil || all.ProviderQuantity == nil || all.ProviderReported == nil {
		t.Fatalf("independent valuations=%+v", all)
	}
	if all.Expected.Totals[0].Amount.CanonicalString() != "4/0" || all.ProviderQuantity.Totals[0].Amount.CanonicalString() != "6/0" {
		t.Fatalf("E/Q totals=%s/%s", all.Expected.Totals[0].Amount, all.ProviderQuantity.Totals[0].Amount)
	}
	if all.ProviderReported.Totals[0].Amount.CanonicalString() != "9/0" {
		t.Fatalf("P total=%s", all.ProviderReported.Totals[0].Amount)
	}
	if all.ProviderReported.Tariff.ID != "" || all.ProviderReported.Rater.ID != "" {
		t.Fatalf("P retained local rating context: tariff=%+v rater=%+v", all.ProviderReported.Tariff, all.ProviderReported.Rater)
	}
	if len(all.Expected.InputObservations) != 1 || all.Expected.InputObservations[0].ObservationID != "local" {
		t.Fatalf("E refs=%+v, want local-only", all.Expected.InputObservations)
	}
	if len(all.ProviderQuantity.InputObservations) != 1 || all.ProviderQuantity.InputObservations[0].ObservationID != "provider" {
		t.Fatalf("Q refs=%+v, want provider-quantity-only", all.ProviderQuantity.InputObservations)
	}
	if len(all.ProviderReported.InputObservations) != 1 || all.ProviderReported.InputObservations[0].ObservationID != "provider" {
		t.Fatalf("P refs=%+v, want provider-charge-only", all.ProviderReported.InputObservations)
	}
}

func TestPhase9ReferenceRater_OneIncompletePlaneDoesNotSuppressOtherPlanes(t *testing.T) {
	t.Parallel()
	localKey := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	providerOnlyKey := phase9Key(metering.DirectionOutput, metering.ComponentAudio, metering.UnitSecond)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", localKey, "2")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	local := phase9Observation(t, "local", metering.OriginLocal, localKey, "2")
	provider := phase9Observation(t, "provider", metering.OriginProvider, providerOnlyKey, "3")
	provider.Charges = []metering.ReportedCharge{{ChargeItemID: "p", Component: &localKey, Amount: phase9Decimal("9"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	base := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{local, provider}, tariff)
	all, err := RateIndependentValuations(context.Background(), rater, base)
	if !errors.Is(err, ErrRateMissing) {
		t.Fatalf("independent partial error=%v, want missing provider rate", err)
	}
	if all.Expected == nil || all.ProviderQuantity == nil || all.ProviderReported == nil {
		t.Fatalf("one failed plane suppressed another=%+v", all)
	}
	if all.ProviderReported.Totals[0].Amount == nil || all.ProviderReported.Totals[0].Amount.CanonicalString() != "9/0" {
		t.Fatalf("provider reported total=%v, want 9/0", all.ProviderReported.Totals)
	}
}

func TestPhase9ProviderReportedRatingDoesNotRequireLocalTariff(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	observation := phase9Observation(t, "provider-only", metering.OriginProvider, key, "1")
	observation.Charges = []metering.ReportedCharge{{
		ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent,
	}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{observation})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	got, err := RateProviderReported(context.Background(), input)
	if err != nil {
		t.Fatalf("RateProviderReported: %v", err)
	}
	if got.Tariff.ID != "" || got.Lines[0].Status != economics.RatingLineProviderReported {
		t.Fatalf("provider-only valuation=%+v", got)
	}
}

func TestPhase9ReferenceRater_CustomerPolicyCanOmitTariffReference(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "2")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisCustomerPolicy, []metering.Observation{
		phase9Observation(t, "customer-policy", metering.OriginLocal, key, "2"),
	}, tariff)
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.Policy = economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy", Version: "v1"}, PolicyID: "customer"}
	input.PolicyContent = &economics.SnapshotContentRef{ContentRef: "catalog://policy/v1", ContentHash: strings.Repeat("5", 64)}
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("customer-policy Rate: %v", err)
	}
	if got.Basis != economics.BasisCustomerPolicy || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "4/0" {
		t.Fatalf("customer-policy valuation=%+v", got)
	}
}

func TestPhase9ReferenceRater_MissingAndIncompleteNeverBecomeZero(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	for _, tc := range []struct {
		name string
		make func() economics.TariffSnapshot
		want error
	}{
		{name: "missing rate", make: func() economics.TariffSnapshot { return phase9Tariff(t, nil) }, want: ErrRateMissing},
		{name: "currency mismatch", make: func() economics.TariffSnapshot {
			return phase9Tariff(t, []economics.RatingRule{{ID: "bad-currency", Component: &key, Currency: "EUR", UnitPrice: phase9Decimal("2")}})
		}, want: ErrRateCurrencyMismatch},
		{name: "incomplete quantity", make: func() economics.TariffSnapshot {
			return phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "2")})
		}, want: ErrQuantityIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			observation := phase9Observation(t, tc.name, metering.OriginLocal, key, "2")
			if tc.name == "incomplete quantity" {
				observation.Measures[0].Value = nil
				observation.Measures[0].Quality = metering.QualityUnavailable
			}
			tariff := tc.make()
			rater, err := NewReferenceRater(tariff)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
			_, err = rater.Rate(context.Background(), input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Rate error=%v, want %v", err, tc.want)
			}
			if strings.Contains(strings.ToLower(err.Error()), "zero") && tc.name != "missing rate" {
				t.Fatalf("diagnostic incorrectly reports zero: %v", err)
			}
		})
	}
}

func TestPhase9ReferenceRater_WholeContextTierAndCoverageFailClosed(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{
		{
			ID: "standard", Component: &key, Currency: "USD", SelectionScope: economics.SelectionWholeContext,
			Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}},
			Tiers:      []economics.RatingTier{{UpTo: phase9Decimal("10"), UnitPrice: phase9Decimal("1")}, {UnitPrice: phase9Decimal("2")}}, TierMode: economics.TierAllUnits,
		},
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{phase9Observation(t, "image", metering.OriginLocal, key, "5")}, tariff)
	input.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}}
	if _, err := rater.Rate(context.Background(), input); err != nil {
		t.Fatalf("qualified tier: %v", err)
	}
	input.EffectiveQualifiers = nil
	if _, err := rater.Rate(context.Background(), input); !errors.Is(err, ErrQualifierMissing) {
		t.Fatalf("missing qualifier error=%v, want %v", err, ErrQualifierMissing)
	}
	bad := phase9Observation(t, "bad", metering.OriginProvider, key, "1")
	bad.Charges = []metering.ReportedCharge{{ChargeItemID: "parent", Amount: phase9Decimal("3"), Currency: "USD", Kind: metering.ChargeKindAggregate}}
	bad.Charges[0].Covers = []metering.ChargeCoverageRef{{Ref: metering.ChargeRef{StoreID: "store", ObservationID: "bad", Revision: 1, ChargeItemID: "child"}, Relation: metering.CoverageAdditive}}
	bad.Charges = append(bad.Charges, metering.ReportedCharge{ChargeItemID: "child", Amount: phase9Decimal("2"), Currency: "USD", Kind: metering.ChargeKindComponent, Component: &key})
	pInput := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{bad}, tariff)
	pInput.Tariff = economics.RatingSnapshotRef{}
	pInput.TariffContent = nil
	pInput.RaterContent = nil
	if _, err := rater.Rate(context.Background(), pInput); !errors.Is(err, ErrCoverageInvalid) {
		t.Fatalf("coverage error=%v, want %v", err, ErrCoverageInvalid)
	}
}

func TestPhase9ReferenceRater_UsesAvailableLessSpecificRule(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{
		{ID: "region", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}},
		{ID: "region-tier", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("2"), Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}, {Name: "service_tier", Value: "premium"}}},
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{phase9Observation(t, "specificity", metering.OriginLocal, key, "2")}, tariff)
	input.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}}
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("Rate with available less-specific rule: %v", err)
	}
	if got.Lines[0].RuleID != "region" {
		t.Fatalf("selected rule=%q, want region", got.Lines[0].RuleID)
	}
}

func TestPhase9ReferenceRater_WholeContextThresholdIncludesSiblingInputUnits(t *testing.T) {
	t.Parallel()
	uncached := phase9Key(metering.DirectionInput, metering.ComponentInputTokenUncached, metering.UnitToken)
	cached := phase9Key(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken)
	tiers := []economics.RatingTier{{UpTo: phase9Decimal("5"), UnitPrice: phase9Decimal("1")}, {UnitPrice: phase9Decimal("2")}}
	whole := phase9Tariff(t, []economics.RatingRule{
		{ID: "uncached-whole", Component: &uncached, Currency: "USD", SelectionScope: economics.SelectionWholeContext, TierMode: economics.TierAllUnits, Tiers: tiers},
		phase9LinearRule("cached-free", cached, "0"),
	})
	rater, err := NewReferenceRater(whole)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "uncached", metering.OriginLocal, uncached, "4"),
		phase9Observation(t, "cached", metering.OriginLocal, cached, "4"),
	}, whole)
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("whole-context Rate: %v", err)
	}
	for _, line := range got.Lines {
		if line.RuleID == "uncached-whole" {
			if line.Amount == nil || line.Amount.CanonicalString() != "8/0" {
				t.Fatalf("whole-context amount=%v, want 8/0 from eight input tokens", line.Amount)
			}
			return
		}
	}
	t.Fatal("whole-context rule line not found")
}

func TestPhase9ReferenceRater_InclusiveCoverageAcrossObservationsIsNotDoubleCounted(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	parent := phase9Observation(t, "aggregate", metering.OriginProvider, key, "1")
	parent.Charges = []metering.ReportedCharge{{
		ChargeItemID: "aggregate", Amount: phase9Decimal("3"), Currency: "USD", Kind: metering.ChargeKindAggregate,
		Covers: []metering.ChargeCoverageRef{{
			Ref: metering.ChargeRef{StoreID: "store", ObservationID: "component", Revision: 1, ChargeItemID: "component"}, Relation: metering.CoverageInclusive,
		}},
	}}
	child := phase9Observation(t, "component", metering.OriginProvider, key, "1")
	child.Charges = []metering.ReportedCharge{{
		ChargeItemID: "component", Component: &key, Amount: phase9Decimal("2"), Currency: "USD", Kind: metering.ChargeKindComponent,
	}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{parent, child})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, err := RateProviderReported(context.Background(), input)
	if err != nil {
		t.Fatalf("RateProviderReported: %v", err)
	}
	if len(got.Lines) != 1 || !got.Lines[0].ReportedAggregate {
		t.Fatalf("lines=%+v, want only covered aggregate", got.Lines)
	}
	if got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "3/0" {
		t.Fatalf("total=%v, want aggregate amount 3/0", got.Totals)
	}
	uncoveredParent := phase9Observation(t, "uncovered-aggregate", metering.OriginProvider, key, "1")
	uncoveredParent.Charges = []metering.ReportedCharge{{
		ChargeItemID: "aggregate", Amount: phase9Decimal("3"), Currency: "USD", Kind: metering.ChargeKindAggregate,
	}}
	uncoveredChild := phase9Observation(t, "uncovered-component", metering.OriginProvider, key, "1")
	uncoveredChild.Charges = []metering.ReportedCharge{{
		ChargeItemID: "component", Component: &key, Amount: phase9Decimal("2"), Currency: "USD", Kind: metering.ChargeKindComponent,
	}}
	_, err = RateProviderReported(context.Background(), phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{uncoveredParent, uncoveredChild}))
	if !errors.Is(err, ErrCoverageInvalid) {
		t.Fatalf("uncovered cross-observation component error=%v, want %v", err, ErrCoverageInvalid)
	}
}

func TestPhase9ReferenceRater_WholeContextExcludesInformationalInputTotal(t *testing.T) {
	t.Parallel()
	uncached := phase9Key(metering.DirectionInput, metering.ComponentInputTokenUncached, metering.UnitToken)
	cached := phase9Key(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken)
	total := phase9Key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)
	tiers := []economics.RatingTier{{UpTo: phase9Decimal("10"), UnitPrice: phase9Decimal("1")}, {UnitPrice: phase9Decimal("2")}}
	tariff := phase9Tariff(t, []economics.RatingRule{
		{ID: "uncached-whole", Component: &uncached, Currency: "USD", SelectionScope: economics.SelectionWholeContext, TierMode: economics.TierAllUnits, Tiers: tiers},
		phase9LinearRule("cached-free", cached, "0"),
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "uncached", metering.OriginLocal, uncached, "4"),
		phase9Observation(t, "cached", metering.OriginLocal, cached, "4"),
		phase9Observation(t, "total", metering.OriginLocal, total, "8"),
	}, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	for _, line := range got.Lines {
		if line.RuleID == "uncached-whole" {
			if line.Amount == nil || line.Amount.CanonicalString() != "4/0" {
				t.Fatalf("whole-context amount=%v, want 4/0 from uncached plus cache context", line.Amount)
			}
			return
		}
	}
	t.Fatal("whole-context rule line not found")
}

func TestPhase9ReferenceRater_ReducesShuffledCumulativeAndLaterDelta(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	base := phase9Observation(t, "cumulative", metering.OriginLocal, key, "10")
	base.Semantics = metering.SemanticsCumulative
	base.Sequence = 1
	delta := phase9Observation(t, "delta", metering.OriginLocal, key, "2")
	delta.Sequence = 2
	for _, observations := range [][]metering.Observation{{delta, base}, {base, delta}} {
		input := phase9RatingInput(t, economics.BasisLocalExpected, observations, tariff)
		got, err := rater.Rate(context.Background(), input)
		if err != nil {
			t.Fatalf("Rate(%v): %v", observations[0].ID, err)
		}
		if got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "12/0" {
			t.Fatalf("order %q total=%v, want cumulative 10 plus later delta 2", observations[0].ID, got.Totals)
		}
	}
}

func TestPhase9ReferenceRater_ReducesSameComponentAcrossIndependentStreams(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	first := phase9Observation(t, "stream-one", metering.OriginLocal, key, "2")
	second := phase9Observation(t, "stream-two", metering.OriginLocal, key, "3")
	second.StreamID = "stream-two"
	second.SourceEventKey = second.ID
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{second, first}, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("lines=%d, want one independently reduced line per stream", len(got.Lines))
	}
	if got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "5/0" {
		t.Fatalf("total=%v, want 5/0", got.Totals)
	}
	for _, line := range got.Lines {
		if len(line.SourceObservationRefs) != 1 {
			t.Fatalf("line %q refs=%+v, want one source ref", line.ID, line.SourceObservationRefs)
		}
	}
}

func TestPhase9ReferenceRater_ReducesSupersedingCorrectionDeterministically(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	base := phase9Observation(t, "base", metering.OriginLocal, key, "10")
	base.Semantics = metering.SemanticsCumulative
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase9Observation(t, "correction", metering.OriginLocal, key, "-3")
	correction.Semantics = metering.SemanticsCorrection
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{correction, base}, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "7/0" {
		t.Fatalf("total=%v, want superseding correction 10-3", got.Totals)
	}
	if len(got.Lines[0].SourceObservationRefs) != 2 {
		t.Fatalf("refs=%+v, want both immutable source observations", got.Lines[0].SourceObservationRefs)
	}
}

func TestPhase9ReferenceRater_AllowsSubNanoUnitPriceUntilFinalRounding(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{{ID: "subnano", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("0.0000000001")}})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "subnano", metering.OriginLocal, key, "10"),
	}, tariff))
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if got.Lines[0].Amount == nil || got.Lines[0].Amount.CanonicalString() != "1/9" {
		t.Fatalf("exact amount=%v, want 1/9", got.Lines[0].Amount)
	}
	if got.Lines[0].RoundedAmount == nil || got.Lines[0].RoundedAmount.NanoUnits != 1 {
		t.Fatalf("rounded amount=%v, want one nano", got.Lines[0].RoundedAmount)
	}
}

func TestPhase9ReferenceRater_RejectsUnrepresentableRoundedMoney(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	huge := strings.Repeat("9", metering.MaxDecimalCoefficientDigits)
	tariff := phase9Tariff(t, []economics.RatingRule{{ID: "huge", Component: &key, Currency: "USD", UnitPrice: phase9Decimal(huge)}})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	_, err = rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "huge", metering.OriginLocal, key, "1"),
	}, tariff))
	if !errors.Is(err, ErrRatingPrecision) {
		t.Fatalf("Rate error=%v, want %v", err, ErrRatingPrecision)
	}
}

func TestPhase9ReferenceRater_RoundingBoundaryAndPolicyAreAuthoritative(t *testing.T) {
	t.Parallel()
	firstKey := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	secondKey := phase9Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	lineRules := []economics.RatingRule{
		{ID: "line-one", Component: &firstKey, Currency: "USD", UnitPrice: phase9Decimal("0.0000000005"), RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingFloor},
		{ID: "line-two", Component: &secondKey, Currency: "USD", UnitPrice: phase9Decimal("0.0000000005"), RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingFloor},
	}
	lineTariff := phase9Tariff(t, lineRules)
	lineRater, err := NewReferenceRater(lineTariff)
	if err != nil {
		t.Fatalf("line rater: %v", err)
	}
	lineInput := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "line-one", metering.OriginLocal, firstKey, "1"),
		phase9Observation(t, "line-two", metering.OriginLocal, secondKey, "1"),
	}, lineTariff)
	lineGot, err := lineRater.Rate(context.Background(), lineInput)
	if err != nil {
		t.Fatalf("line Rate: %v", err)
	}
	if lineGot.Totals[0].RoundedAmount.NanoUnits != 0 {
		t.Fatalf("line-rounded total=%v, want 0 nanos", lineGot.Totals)
	}

	callRules := make([]economics.RatingRule, len(lineRules))
	copy(callRules, lineRules)
	for i := range callRules {
		callRules[i].RoundingScope = economics.RoundingScopeCall
		callRules[i].RoundingPolicy = economics.RoundingFloor
	}
	callTariff := phase9Tariff(t, callRules)
	callRater, err := NewReferenceRater(callTariff)
	if err != nil {
		t.Fatalf("call rater: %v", err)
	}
	callInput := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "call-one", metering.OriginLocal, firstKey, "1"),
		phase9Observation(t, "call-two", metering.OriginLocal, secondKey, "1"),
	}, callTariff)
	callGot, err := callRater.Rate(context.Background(), callInput)
	if err != nil {
		t.Fatalf("call Rate: %v", err)
	}
	if callGot.Totals[0].RoundedAmount.NanoUnits != 1 {
		t.Fatalf("call-rounded total=%v, want 1 nano", callGot.Totals)
	}
	for _, line := range callGot.Lines {
		if line.RoundedAmount != nil {
			t.Fatalf("call-scoped line %q carried a premature rounded amount: %+v", line.ID, line.RoundedAmount)
		}
	}

	halfEven := phase9Tariff(t, []economics.RatingRule{{
		ID: "half-even", Component: &firstKey, Currency: "USD", UnitPrice: phase9Decimal("0.0000000005"),
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
	}})
	halfRater, err := NewReferenceRater(halfEven)
	if err != nil {
		t.Fatalf("half-even rater: %v", err)
	}
	halfGot, err := halfRater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "half-even", metering.OriginLocal, firstKey, "1"),
	}, halfEven))
	if err != nil {
		t.Fatalf("half-even Rate: %v", err)
	}
	if halfGot.Totals[0].RoundedAmount.NanoUnits != 0 {
		t.Fatalf("half-even total=%v, want 0 nanos", halfGot.Totals)
	}

	period := phase9Tariff(t, []economics.RatingRule{{
		ID: "period-floor", Component: &firstKey, Currency: "USD", UnitPrice: phase9Decimal("0.0000000005"),
		SelectionScope: economics.SelectionPeriod, RoundingScope: economics.RoundingScopePeriod, RoundingPolicy: economics.RoundingFloor,
	}})
	periodRater, err := NewReferenceRater(period)
	if err != nil {
		t.Fatalf("period rater: %v", err)
	}
	periodInput := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{
		phase9Observation(t, "period-floor", metering.OriginLocal, firstKey, "1"),
	}, period)
	periodInput.Scope = "period:2026-09"
	periodGot, err := periodRater.Rate(context.Background(), periodInput)
	if err != nil {
		t.Fatalf("period Rate: %v", err)
	}
	if periodGot.Lines[0].RoundingScope != economics.RoundingScopePeriod {
		t.Fatalf("period line scope=%q, want period", periodGot.Lines[0].RoundingScope)
	}
	if periodGot.Lines[0].RoundedAmount != nil {
		t.Fatalf("period-scoped line carried a premature rounded amount: %+v", periodGot.Lines[0].RoundedAmount)
	}
	if periodGot.Totals[0].RoundedAmount.NanoUnits != 0 {
		t.Fatalf("period total=%v, want period floor 0 nanos", periodGot.Totals)
	}
}

func TestPhase9ReferenceRater_RejectsContradictoryKindsAndEqualSpecificityOverlap(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tests := []struct {
		name  string
		rules []economics.RatingRule
	}{
		{name: "fixed component price", rules: []economics.RatingRule{{ID: "fixed", Kind: economics.RatingRuleFixed, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1")}}},
		{name: "linear fixed amount", rules: []economics.RatingRule{{ID: "linear", Kind: economics.RatingRuleLinear, Currency: "USD", FixedAmount: phase9Decimal("1"), FixedScope: economics.FixedFeeScopeCall}}},
		{name: "all units without matching tiers", rules: []economics.RatingRule{{ID: "all", Kind: economics.RatingRuleAllUnits, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), TierMode: economics.TierGraduated}}},
		{name: "graduated without matching tiers", rules: []economics.RatingRule{{ID: "graduated", Kind: economics.RatingRuleGraduated, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), TierMode: economics.TierAllUnits}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tariff := economics.TariffSnapshot{
				Ref:      economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-invalid", Version: "v1"}, RaterID: "reference"},
				Currency: "USD", Rules: tc.rules,
			}
			if _, err := NewReferenceRater(tariff); !errors.Is(err, economics.ErrInvalidRatingRule) {
				t.Fatalf("publication error=%v, want %v", err, economics.ErrInvalidRatingRule)
			}
		})
	}
	first := key
	first.Dimensions = []metering.Dimension{{Name: "media", Value: "image"}}
	second := key
	second.Dimensions = nil
	overlap := economics.TariffSnapshot{
		Ref:      economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-overlap", Version: "v1"}, RaterID: "reference"},
		Currency: "USD", Rules: []economics.RatingRule{
			{ID: "dimension-specific", Component: &first, Currency: "USD", UnitPrice: phase9Decimal("1"), Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}}},
			{ID: "qualifier-specific", Component: &second, Currency: "USD", UnitPrice: phase9Decimal("2"), Conditions: []economics.QualifierCondition{{Name: "region", Value: "us"}, {Name: "service_tier", Value: "premium"}}},
		},
	}
	if _, err := NewReferenceRater(overlap); !errors.Is(err, economics.ErrRatingRuleOverlap) {
		t.Fatalf("split overlap publication error=%v, want %v", err, economics.ErrRatingRuleOverlap)
	}
}

func TestPhase9RateIndependentValuations_UsesStablePlaneSpecificHashes(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "2")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	local := phase9Observation(t, "local", metering.OriginLocal, key, "2")
	provider := phase9Observation(t, "provider", metering.OriginProvider, key, "3")
	provider.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("9"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	base := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{provider, local}, tariff)
	first, err := RateIndependentValuations(context.Background(), rater, base)
	if err != nil {
		t.Fatalf("first independent rating: %v", err)
	}
	base.Observations = []metering.Observation{local, provider}
	second, err := RateIndependentValuations(context.Background(), rater, base)
	if err != nil {
		t.Fatalf("reordered independent rating: %v", err)
	}
	if first.Expected == nil || first.ProviderQuantity == nil || first.ProviderReported == nil {
		t.Fatalf("first planes=%+v", first)
	}
	if first.Expected.InputSetHash == first.ProviderQuantity.InputSetHash || first.Expected.InputSetHash == first.ProviderReported.InputSetHash || first.ProviderQuantity.InputSetHash == first.ProviderReported.InputSetHash {
		t.Fatalf("plane hashes are not distinct: E=%q Q=%q P=%q", first.Expected.InputSetHash, first.ProviderQuantity.InputSetHash, first.ProviderReported.InputSetHash)
	}
	if first.Expected.InputSetHash != second.Expected.InputSetHash || first.ProviderQuantity.InputSetHash != second.ProviderQuantity.InputSetHash || first.ProviderReported.InputSetHash != second.ProviderReported.InputSetHash {
		t.Fatalf("plane hashes changed with input order: first E/Q/P=%q/%q/%q second=%q/%q/%q", first.Expected.InputSetHash, first.ProviderQuantity.InputSetHash, first.ProviderReported.InputSetHash, second.Expected.InputSetHash, second.ProviderQuantity.InputSetHash, second.ProviderReported.InputSetHash)
	}
	if first.Expected.ID != second.Expected.ID || first.ProviderQuantity.ID != second.ProviderQuantity.ID || first.ProviderReported.ID != second.ProviderReported.ID {
		t.Fatalf("valuation IDs changed with input order: first=%q/%q/%q second=%q/%q/%q", first.Expected.ID, first.ProviderQuantity.ID, first.ProviderReported.ID, second.Expected.ID, second.ProviderQuantity.ID, second.ProviderReported.ID)
	}
}

func TestPhase9ReferenceRater_PeriodScopeCannotFallBackToCallRounding(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{{ID: "period", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), RoundingScope: economics.RoundingScopePeriod, RoundingPolicy: economics.RoundingHalfEven}})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{phase9Observation(t, "period", metering.OriginLocal, key, "1")}, tariff)
	if _, err := rater.Rate(context.Background(), input); !errors.Is(err, ErrPeriodScopeRequired) {
		t.Fatalf("unscoped period error=%v, want %v", err, ErrPeriodScopeRequired)
	}
}

func phase9Key(direction metering.FlowDirection, component, unit string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: "phase9.v1"}
}

func phase9Decimal(value string) *metering.Decimal {
	d, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return &d
}

func phase9LinearRule(id string, key metering.ComponentKey, price string) economics.RatingRule {
	return economics.RatingRule{ID: id, Component: &key, Currency: "USD", UnitPrice: phase9Decimal(price)}
}

func phase9FixedRule(id string, scope economics.FixedFeeScope, amount string) economics.RatingRule {
	return economics.RatingRule{ID: id, Currency: "USD", FixedAmount: phase9Decimal(amount), FixedScope: scope}
}

func phase9Tariff(t *testing.T, rules []economics.RatingRule) economics.TariffSnapshot {
	t.Helper()
	return economics.NewTariffSnapshot(economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-tariff", Version: "v1"}, RaterID: "reference"}, "USD", rules)
}

func phase9RatingInput(t *testing.T, basis economics.ValuationBasis, observations []metering.Observation, tariffs ...economics.TariffSnapshot) economics.RatingInput {
	t.Helper()
	input := economics.RatingInput{
		Version: 2, Perspective: metering.PerspectiveCustomer, Basis: basis,
		Subject: observations[0].Subject, Scope: "call:phase9", Observations: observations,
		Rater:         economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-rater", Version: "v1"}, RaterID: "reference"},
		RaterContent:  &economics.SnapshotContentRef{ContentRef: "catalog://rater/v1", ContentHash: strings.Repeat("1", 64)},
		Tariff:        economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-tariff", Version: "v1"}, RaterID: "reference"},
		TariffContent: &economics.SnapshotContentRef{ContentRef: "catalog://tariff/v1", ContentHash: strings.Repeat("2", 64)},
		// The trusted rater derives the canonical input-set identity from the
		// retained observation refs; fixtures leave it empty to exercise that
		// boundary instead of supplying a synthetic hash.
		InputSetHash: "", QualifierSnapshotRef: &economics.SnapshotContentRef{ContentRef: "catalog://qualifiers/v1", ContentHash: strings.Repeat("4", 64)},
		AsOf: time.Unix(100, 0).UTC(),
	}
	if len(tariffs) != 0 {
		input.Tariff = tariffs[0].Ref
		input.TariffContent = &tariffs[0].Content
	}
	return input
}

func phase9Observation(t *testing.T, id string, origin string, key metering.ComponentKey, quantity string) metering.Observation {
	t.Helper()
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store", ALegID: "a", BillingCallID: "call", BLegID: "b"}
	acquisition := metering.AcquisitionLocalTransport
	if origin == metering.OriginProvider {
		acquisition = metering.AcquisitionProviderResponse
	}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id, Revision: 1, StreamID: "stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveCustomer,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: "store", ALegID: "a", BillingCallID: "call", BLegID: "b"},
		Semantics:   metering.SemanticsDelta, ObservedAt: time.Unix(90, 0).UTC(), ReceivedAt: time.Unix(91, 0).UTC(), MappingRef: "phase9",
		Measures: []metering.Measure{{Key: key, Value: phase9Decimal(quantity), Quality: metering.QualityObserved}},
	}
}
