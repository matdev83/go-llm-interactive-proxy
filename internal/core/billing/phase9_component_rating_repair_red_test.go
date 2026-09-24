package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair_ReplayedObservationDoesNotChangePlaneIdentity(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "replayed", metering.OriginLocal, key, "2")
	singleInput := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
	single, singleErr := RateIndependentValuations(context.Background(), rater, singleInput)
	if singleErr != nil {
		t.Fatalf("single rating: %v", singleErr)
	}
	replayedInput := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation, observation.Clone()}, tariff)
	replayed, replayedErr := RateIndependentValuations(context.Background(), rater, replayedInput)
	if replayedErr != nil {
		t.Fatalf("identical replay rating: %v", replayedErr)
	}
	if single.Expected == nil || replayed.Expected == nil {
		t.Fatalf("expected valuations missing: single=%+v replayed=%+v", single, replayed)
	}
	if len(replayed.Expected.InputObservations) != 1 {
		t.Fatalf("replayed input refs=%+v, want one canonical ref", replayed.Expected.InputObservations)
	}
	if replayed.Expected.InputSetHash != single.Expected.InputSetHash || replayed.Expected.ID != single.Expected.ID {
		t.Fatalf("replay changed identity: single hash/id=%q/%q replay hash/id=%q/%q", single.Expected.InputSetHash, single.Expected.ID, replayed.Expected.InputSetHash, replayed.Expected.ID)
	}
}

func TestPhase9Repair_GenuineRevisionRemainsAuditReference(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	first := phase9Observation(t, "revision-one", metering.OriginLocal, key, "2")
	first.SourceEventKey = "same-source-event"
	first.Sequence = 1
	second := phase9Observation(t, "revision-two", metering.OriginLocal, key, "3")
	second.SourceEventKey = first.SourceEventKey
	second.Revision = 2
	second.Sequence = 2
	second.Semantics = metering.SemanticsCumulative
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{second, first}, tariff)
	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("genuine revision rating: %v", err)
	}
	if len(got.InputObservations) != 2 {
		t.Fatalf("input refs=%+v, want both genuine revisions", got.InputObservations)
	}
	if len(got.Lines) != 1 || len(got.Lines[0].SourceObservationRefs) != 2 {
		t.Fatalf("line refs=%+v, want both revision refs", got.Lines)
	}
}

func TestPhase9Repair_ReplayedProviderPlanesKeepStableHashes(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	provider := phase9Observation(t, "provider-replayed", metering.OriginProvider, key, "2")
	provider.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("3"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	single, err := RateIndependentValuations(context.Background(), rater, phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{provider}, tariff))
	if err != nil {
		t.Fatalf("single provider planes: %v", err)
	}
	replayed, err := RateIndependentValuations(context.Background(), rater, phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{provider, provider.Clone()}, tariff))
	if err != nil {
		t.Fatalf("replayed provider planes: %v", err)
	}
	for _, plane := range []struct {
		name string
		one  *economics.Valuation
		two  *economics.Valuation
	}{
		{name: "Q", one: single.ProviderQuantity, two: replayed.ProviderQuantity},
		{name: "P", one: single.ProviderReported, two: replayed.ProviderReported},
	} {
		if plane.one == nil || plane.two == nil {
			t.Fatalf("%s plane missing: single=%+v replayed=%+v", plane.name, single, replayed)
		}
		if plane.one.InputSetHash != plane.two.InputSetHash || plane.one.ID != plane.two.ID || len(plane.two.InputObservations) != 1 {
			t.Fatalf("%s replay identity changed: single=%q/%q replay=%q/%q refs=%+v", plane.name, plane.one.InputSetHash, plane.one.ID, plane.two.InputSetHash, plane.two.ID, plane.two.InputObservations)
		}
	}
}

func TestPhase9Repair_SupersededIncompleteMeasureDoesNotPoisonCompleteValue(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	stale := phase9Observation(t, "stale", metering.OriginLocal, key, "99")
	stale.Sequence = 1
	stale.Measures[0].Value = nil
	stale.Measures[0].Quality = metering.QualityUnavailable
	staleRef, err := stale.Ref(stale.Subject.StoreID)
	if err != nil {
		t.Fatalf("stale ref: %v", err)
	}
	fresh := phase9Observation(t, "fresh", metering.OriginLocal, key, "2")
	fresh.Sequence = 2
	fresh.Semantics = metering.SemanticsReplacement
	fresh.Supersedes = []metering.ObservationRef{staleRef}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{stale, fresh}, tariff))
	if err != nil {
		t.Fatalf("superseding complete rating: %v", err)
	}
	if got.Completeness != economics.CompletenessComplete || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "2/0" {
		t.Fatalf("valuation=%+v, want complete fresh value", got)
	}
}

func TestPhase9Repair_UnresolvedMeasureCorrectionIsIncompleteAndUnchargeable(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	correction := phase9Observation(t, "unresolved-correction", metering.OriginLocal, key, "-3")
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{{StoreID: "store", ObservationID: "missing", Revision: 1, PayloadHash: strings.Repeat("a", 64)}}
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{correction}, tariff))
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("unresolved correction error=%v, want %v", err, ErrRatingEvidenceMissing)
	}
	if got.Completeness == economics.CompletenessComplete || len(got.Totals) != 0 {
		t.Fatalf("unresolved correction valuation=%+v, want incomplete and unchargeable", got)
	}
}

func TestPhase9Repair_ProviderCorrectionReplacesBaseWithoutDoubleBilling(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	base := phase9Observation(t, "provider-base", metering.OriginProvider, key, "1")
	base.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("3"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase9Observation(t, "provider-correction", metering.OriginProvider, key, "1")
	correction.Sequence = 2
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("4"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{correction, base, base.Clone()})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, err := RateProviderReported(context.Background(), input)
	if err != nil {
		t.Fatalf("provider correction rating: %v", err)
	}
	if len(got.Lines) != 1 || got.Totals[0].Amount == nil || got.Totals[0].Amount.CanonicalString() != "4/0" {
		t.Fatalf("provider correction valuation=%+v, want one corrected charge totaling 4", got)
	}
}

func TestPhase9Repair_UnresolvedProviderCorrectionIsIncomplete(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	correction := phase9Observation(t, "provider-unresolved", metering.OriginProvider, key, "1")
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{{StoreID: "store", ObservationID: "provider-missing", Revision: 1, PayloadHash: strings.Repeat("b", 64)}}
	correction.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-charge", Component: &key, Amount: phase9Decimal("4"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{correction})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, err := RateProviderReported(context.Background(), input)
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("unresolved provider correction error=%v, want %v", err, ErrRatingEvidenceMissing)
	}
	if got.Completeness == economics.CompletenessComplete || len(got.Totals) != 0 {
		t.Fatalf("unresolved provider correction valuation=%+v, want incomplete and unchargeable", got)
	}
}

func TestPhase9Repair_ProviderTotalsHonorLineRounding(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	first := phase9Observation(t, "provider-half-one", metering.OriginProvider, key, "1")
	first.Charges = []metering.ReportedCharge{{ChargeItemID: "charge-one", Component: &key, Amount: phase9Decimal("0.0000000005"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	second := phase9Observation(t, "provider-half-two", metering.OriginProvider, key, "1")
	second.Charges = []metering.ReportedCharge{{ChargeItemID: "charge-two", Component: &key, Amount: phase9Decimal("0.0000000005"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{first, second})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, err := RateProviderReported(context.Background(), input)
	if err != nil {
		t.Fatalf("provider line rounding: %v", err)
	}
	if got.Totals[0].RoundedAmount.NanoUnits != 2 {
		t.Fatalf("provider totals=%v, want two independently rounded nanos", got.Totals)
	}
}

func TestPhase9Repair_ProviderAggregateRoundingOverflowIsTyped(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	maxMoney := "9223372036.854775807"
	observations := make([]metering.Observation, 0, 2)
	for _, id := range []string{"provider-max-one", "provider-max-two"} {
		observation := phase9Observation(t, id, metering.OriginProvider, key, "1")
		observation.Charges = []metering.ReportedCharge{{ChargeItemID: id + "-charge", Component: &key, Amount: phase9Decimal(maxMoney), Currency: "USD", Kind: metering.ChargeKindComponent}}
		observations = append(observations, observation)
	}
	input := phase9RatingInput(t, economics.BasisProviderReported, observations)
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	_, err := RateProviderReported(context.Background(), input)
	if !errors.Is(err, ErrRatingPrecision) {
		t.Fatalf("provider aggregate overflow error=%v, want %v", err, ErrRatingPrecision)
	}
}

func TestPhase9Repair_ExplicitRatingKindsRequireUnambiguousFields(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	validTier := []economics.RatingTier{{UnitPrice: phase9Decimal("1")}}
	tests := []struct {
		name string
		rule economics.RatingRule
	}{
		{name: "linear missing rate", rule: economics.RatingRule{ID: "linear-missing-rate", Kind: economics.RatingRuleLinear, Component: &key, Currency: "USD"}},
		{name: "block missing block size", rule: economics.RatingRule{ID: "block-missing-size", Kind: economics.RatingRuleBlock, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1")}},
		{name: "block carries tiers", rule: economics.RatingRule{ID: "block-tiers", Kind: economics.RatingRuleBlock, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), BlockSize: phase9Decimal("1"), TierMode: economics.TierAllUnits, Tiers: validTier}},
		{name: "minimum missing amount", rule: economics.RatingRule{ID: "minimum-missing-amount", Kind: economics.RatingRuleMinimum, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1")}},
		{name: "all units carries direct rate", rule: economics.RatingRule{ID: "all-direct-rate", Kind: economics.RatingRuleAllUnits, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), TierMode: economics.TierAllUnits, Tiers: validTier}},
		{name: "graduated carries direct rate", rule: economics.RatingRule{ID: "graduated-direct-rate", Kind: economics.RatingRuleGraduated, Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), TierMode: economics.TierGraduated, Tiers: validTier}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tariff := economics.TariffSnapshot{Ref: economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "phase9-kind-invalid", Version: tc.name}, RaterID: "reference"}, Currency: "USD", Rules: []economics.RatingRule{tc.rule}}
			if _, err := NewReferenceRater(tariff); !errors.Is(err, economics.ErrInvalidRatingRule) {
				t.Fatalf("publication error=%v, want %v", err, economics.ErrInvalidRatingRule)
			}
		})
	}
}

func TestPhase9Repair_FixedFeeScopeMustMatchValuationScope(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	observation := phase9Observation(t, "fixed-scope", metering.OriginLocal, key, "1")
	for _, tc := range []struct {
		feeScope   economics.FixedFeeScope
		valueScope string
	}{
		{feeScope: economics.FixedFeeScopeSubmission, valueScope: "call:call"},
		{feeScope: economics.FixedFeeScopeSubmission, valueScope: "period:period"},
		{feeScope: economics.FixedFeeScopeCall, valueScope: "submission:submission"},
		{feeScope: economics.FixedFeeScopeCall, valueScope: "period:period"},
		{feeScope: economics.FixedFeeScopePeriod, valueScope: "submission:submission"},
		{feeScope: economics.FixedFeeScopePeriod, valueScope: "call:call"},
	} {
		t.Run(string(tc.feeScope)+"-on-"+tc.valueScope, func(t *testing.T) {
			tariff := phase9Tariff(t, []economics.RatingRule{
				phase9LinearRule("image", key, "1"),
				phase9FixedRule("fixed", tc.feeScope, "1"),
			})
			rater, err := NewReferenceRater(tariff)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
			input.Scope = tc.valueScope
			if _, err := rater.Rate(context.Background(), input); !errors.Is(err, ErrFixedFeeScopeMismatch) {
				t.Fatalf("scope mismatch error=%v, want %v", err, ErrFixedFeeScopeMismatch)
			}
		})
	}
}

func TestPhase9Repair_ValuationRejectsRoundedAmountOutsideLineScope(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	for _, scope := range []economics.RoundingScope{economics.RoundingScopeCall, economics.RoundingScopePeriod} {
		t.Run(string(scope), func(t *testing.T) {
			tariff := phase9Tariff(t, []economics.RatingRule{{ID: "scoped", Component: &key, Currency: "USD", UnitPrice: phase9Decimal("1"), SelectionScope: func() economics.RatingSelectionScope {
				if scope == economics.RoundingScopePeriod {
					return economics.SelectionPeriod
				}
				return economics.SelectionBillableQuantity
			}(), RoundingScope: scope, RoundingPolicy: economics.RoundingHalfEven}})
			rater, err := NewReferenceRater(tariff)
			if err != nil {
				t.Fatalf("NewReferenceRater: %v", err)
			}
			input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{phase9Observation(t, "rounded-"+string(scope), metering.OriginLocal, key, "1")}, tariff)
			if scope == economics.RoundingScopePeriod {
				input.Scope = "period:phase9"
			}
			got, err := rater.Rate(context.Background(), input)
			if err != nil {
				t.Fatalf("Rate: %v", err)
			}
			money := economics.Money{Currency: "USD", NanoUnits: 1, Present: true}
			got.Lines[0].RoundedAmount = &money
			if err := got.Validate(); !errors.Is(err, economics.ErrInvalidValuation) {
				t.Fatalf("validation error=%v, want %v", err, economics.ErrInvalidValuation)
			}
		})
	}
}

func TestPhase9Repair_EmptyUnavailableAuthorityClaimCreatesApplicablePlane(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "unavailable", metering.OriginLocal, key, "1")
	observation.Authority = metering.AuthorityUnavailableClaim
	observation.Measures = nil
	all, err := RateIndependentValuations(context.Background(), rater, phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff))
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("unavailable authority error=%v, want %v", err, ErrRatingEvidenceMissing)
	}
	if all.Expected == nil || all.Expected.Completeness == economics.CompletenessComplete {
		t.Fatalf("unavailable authority plane=%+v, want typed incomplete valuation", all.Expected)
	}
}
