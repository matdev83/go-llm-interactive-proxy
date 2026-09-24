package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair6_DirectRaterVerifiesAndFillsInputSetHash(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "2")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "repair6-hash", metering.OriginLocal, key, "3")
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
	input.InputSetHash = ""

	got, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("empty caller hash should be filled by the rater: %v", err)
	}
	refs, err := inputObservationRefs(input)
	if err != nil {
		t.Fatalf("input refs: %v", err)
	}
	want := hashPlaneObservationRefs(input.Basis, refs)
	if got.InputSetHash != want {
		t.Fatalf("input set hash=%q, want canonical %q", got.InputSetHash, want)
	}

	input.InputSetHash = strings.Repeat("a", 64)
	_, err = rater.Rate(context.Background(), input)
	if !errors.Is(err, ErrInputSetHashMismatch) {
		t.Fatalf("mismatched caller hash error=%v, want ErrInputSetHashMismatch", err)
	}
}

func TestPhase9Repair6_IndependentRaterPropagatesHashMismatch(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "2")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "repair6-independent-hash", metering.OriginLocal, key, "3")
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
	input.InputSetHash = strings.Repeat("c", 64)
	_, err = RateIndependentValuations(context.Background(), rater, input)
	if !errors.Is(err, ErrInputSetHashMismatch) {
		t.Fatalf("independent mismatched caller hash error=%v, want ErrInputSetHashMismatch", err)
	}
}

func TestPhase9Repair6_InputSetHashIsOrderStableAndSetSpecific(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	first := phase9Observation(t, "repair6-first", metering.OriginLocal, key, "1")
	second := phase9Observation(t, "repair6-second", metering.OriginLocal, key, "2")
	first.Sequence = 1
	second.Sequence = 2
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{first, second}, tariff)
	input.InputSetHash = ""
	forward, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("forward rate: %v", err)
	}
	input.Observations = []metering.Observation{second, first}
	input.InputSetHash = ""
	reverse, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("reverse rate: %v", err)
	}
	if forward.InputSetHash != reverse.InputSetHash || forward.ID != reverse.ID {
		t.Fatalf("reordering changed identity: forward=%q/%q reverse=%q/%q", forward.InputSetHash, forward.ID, reverse.InputSetHash, reverse.ID)
	}

	input.Observations = []metering.Observation{first}
	input.InputSetHash = forward.InputSetHash
	_, err = rater.Rate(context.Background(), input)
	if !errors.Is(err, ErrInputSetHashMismatch) {
		t.Fatalf("distinct observation set error=%v, want ErrInputSetHashMismatch", err)
	}
}

func TestPhase9Repair6_CorrectionRequiresEverySameFieldParentAndIsOrderIndependent(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	usable := phase9Observation(t, "repair6-usable-parent", metering.OriginLocal, key, "2")
	usable.Sequence = 1
	usable.Semantics = metering.SemanticsCumulative
	unavailable := phase9Observation(t, "repair6-unavailable-parent", metering.OriginLocal, key, "0")
	unavailable.Sequence = 2
	unavailable.Semantics = metering.SemanticsCumulative
	unavailable.Measures[0].Value = nil
	unavailable.Measures[0].Quality = metering.QualityUnavailable
	usableRef, err := usable.Ref(usable.Subject.StoreID)
	if err != nil {
		t.Fatalf("usable ref: %v", err)
	}
	unavailableRef, err := unavailable.Ref(unavailable.Subject.StoreID)
	if err != nil {
		t.Fatalf("unavailable ref: %v", err)
	}
	correction := phase9Observation(t, "repair6-mixed-parent-correction", metering.OriginLocal, key, "1")
	correction.Sequence = 3
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{usableRef, unavailableRef}

	for _, observations := range [][]metering.Observation{
		{correction, unavailable, usable},
		{usable, correction, unavailable},
		{unavailable, usable, correction},
	} {
		input := phase9RatingInput(t, economics.BasisLocalExpected, observations, tariff)
		input.InputSetHash = ""
		got, rateErr := rater.Rate(context.Background(), input)
		if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
			t.Fatalf("observations order=%q rate error=%v, want correction diagnostic", observationIDs(observations), rateErr)
		}
		line := lineForRepair6Key(got, key)
		if line == nil || line.Status != economics.RatingLineQuantityIncomplete {
			t.Fatalf("observations order=%q line=%+v, want quantity-incomplete same-field correction", observationIDs(observations), line)
		}
	}
}

func TestPhase9Repair6_UnknownIntermediateCorrectionRemainsIncompleteTransitively(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("audio", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	unknown := metering.ObservationRef{StoreID: "store", ObservationID: "repair6-late-parent", Revision: 1, PayloadHash: strings.Repeat("b", 64)}
	intermediate := phase9Observation(t, "repair6-intermediate", metering.OriginLocal, key, "1")
	intermediate.Sequence = 1
	intermediate.Semantics = metering.SemanticsCorrection
	intermediate.Supersedes = []metering.ObservationRef{unknown}
	intermediateRef, err := intermediate.Ref(intermediate.Subject.StoreID)
	if err != nil {
		t.Fatalf("intermediate ref: %v", err)
	}
	final := phase9Observation(t, "repair6-final", metering.OriginLocal, key, "1")
	final.Sequence = 2
	final.Semantics = metering.SemanticsCorrection
	final.Supersedes = []metering.ObservationRef{intermediateRef}

	for _, observations := range [][]metering.Observation{{final, intermediate}, {intermediate, final}} {
		input := phase9RatingInput(t, economics.BasisLocalExpected, observations, tariff)
		input.InputSetHash = ""
		got, rateErr := rater.Rate(context.Background(), input)
		if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
			t.Fatalf("observations order=%q rate error=%v, want transitive diagnostic", observationIDs(observations), rateErr)
		}
		line := lineForRepair6Key(got, key)
		if line == nil || line.Status != economics.RatingLineQuantityIncomplete {
			t.Fatalf("observations order=%q line=%+v, want transitive quantity-incomplete correction", observationIDs(observations), line)
		}
	}
}

func TestPhase9Repair6_ProviderChargeCorrectionRequiresEverySameItemParent(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	base := phase9Observation(t, "repair6-charge-usable", metering.OriginProvider, key, "1")
	base.Measures = nil
	base.Semantics = metering.SemanticsCumulative
	base.Charges = []metering.ReportedCharge{{ChargeItemID: "image-charge", Component: &key, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	missing := phase9Observation(t, "repair6-charge-unavailable", metering.OriginProvider, key, "1")
	missing.Measures = nil
	missing.Semantics = metering.SemanticsCumulative
	missing.Charges = []metering.ReportedCharge{{ChargeItemID: "image-charge", Component: &key, Kind: metering.ChargeKindComponent}}
	base.Sequence = 1
	missing.Sequence = 2
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	missingRef, err := missing.Ref(missing.Subject.StoreID)
	if err != nil {
		t.Fatalf("missing ref: %v", err)
	}
	correction := phase9Observation(t, "repair6-charge-correction", metering.OriginProvider, key, "1")
	correction.Measures = nil
	correction.Sequence = 3
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef, missingRef}
	correction.Charges = []metering.ReportedCharge{{ChargeItemID: "image-charge", Component: &key, Amount: phase9Decimal("8"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{correction, missing, base})
	input.InputSetHash = ""
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("provider correction error=%v, want correction diagnostic", rateErr)
	}
	for _, line := range got.Lines {
		if line.ItemID == "image-charge" && line.Status == economics.RatingLineProviderReported {
			t.Fatalf("incomplete corrected provider charge remained payable: %+v", line)
		}
	}
}

func TestPhase9Repair6_ProviderPendingSiblingDoesNotPoisonKnownCharge(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	base := phase9Observation(t, "repair6-charge-sibling-base", metering.OriginProvider, key, "1")
	base.Measures = nil
	base.Semantics = metering.SemanticsCumulative
	base.Charges = []metering.ReportedCharge{{ChargeItemID: "known-charge", Component: &key, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	unknownRef := metering.ObservationRef{StoreID: "store", ObservationID: "repair6-unrelated-charge", Revision: 1, PayloadHash: strings.Repeat("d", 64)}
	correction := phase9Observation(t, "repair6-charge-sibling-correction", metering.OriginProvider, key, "1")
	correction.Measures = nil
	correction.Sequence = 2
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef, unknownRef}
	correction.Charges = []metering.ReportedCharge{{ChargeItemID: "known-charge", Component: &key, Amount: phase9Decimal("8"), Currency: "USD", Kind: metering.ChargeKindComponent}}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{correction, base})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("provider pending sibling error=%v, want pending diagnostic", rateErr)
	}
	if len(got.Lines) != 1 || got.Lines[0].ItemID != "known-charge" || got.Lines[0].Status != economics.RatingLineProviderReported || got.Lines[0].Amount == nil || got.Lines[0].Amount.CanonicalString() != "8/0" {
		t.Fatalf("known provider sibling lines=%+v, want corrected amount 8 retained", got.Lines)
	}
}

func TestPhase9Repair6_UnchangedSiblingFieldRemainsIncompleteAfterCorrection(t *testing.T) {
	t.Parallel()
	imageKey := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	audioKey := phase9Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	tariff := phase9Tariff(t, []economics.RatingRule{
		phase9LinearRule("image", imageKey, "1"),
		phase9LinearRule("audio", audioKey, "2"),
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	base := phase9Observation(t, "repair6-sibling-base", metering.OriginLocal, imageKey, "2")
	base.Semantics = metering.SemanticsCumulative
	base.Sequence = 1
	base.Measures = append(base.Measures, metering.Measure{Key: audioKey, Quality: metering.QualityUnavailable})
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase9Observation(t, "repair6-sibling-correction", metering.OriginLocal, imageKey, "1")
	correction.Semantics = metering.SemanticsCorrection
	correction.Sequence = 2
	correction.Supersedes = []metering.ObservationRef{baseRef}
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{correction, base}, tariff)
	input.InputSetHash = ""
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) && !errors.Is(rateErr, ErrQuantityIncomplete) {
		t.Fatalf("correction error=%v, want unchanged sibling diagnostic", rateErr)
	}
	imageLine := lineForRepair6Key(got, imageKey)
	if imageLine == nil || imageLine.Status != economics.RatingLineRated || imageLine.Amount == nil || imageLine.Amount.CanonicalString() != "3/0" {
		t.Fatalf("healthy corrected image line=%+v, want amount 3", imageLine)
	}
	audioLine := lineForRepair6Key(got, audioKey)
	if audioLine == nil || audioLine.Status != economics.RatingLineQuantityIncomplete {
		t.Fatalf("unchanged unavailable audio line=%+v, want quantity-incomplete", audioLine)
	}
}

func TestPhase9Repair6_EffectiveQualifiersArePartOfValuationIdentity(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	us := phase9LinearRule("image-us", key, "2")
	us.Conditions = []economics.QualifierCondition{{Name: "region", Value: "us"}}
	eu := phase9LinearRule("image-eu", key, "3")
	eu.Conditions = []economics.QualifierCondition{{Name: "region", Value: "eu"}}
	tariff := phase9Tariff(t, []economics.RatingRule{us, eu})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "repair6-qualifier", metering.OriginLocal, key, "2")
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
	input.InputSetHash = ""
	input.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}, {Name: "tier", Value: "standard"}}
	usValuation, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("US rate: %v", err)
	}
	input.EffectiveQualifiers = []metering.Dimension{{Name: "tier", Value: "standard"}, {Name: "region", Value: "eu"}}
	input.InputSetHash = ""
	euValuation, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("EU rate: %v", err)
	}
	if usValuation.ID == euValuation.ID || usValuation.ContextHash() == euValuation.ContextHash() {
		t.Fatalf("different effective qualifiers collapsed identity: US=%q/%q EU=%q/%q", usValuation.ID, usValuation.ContextHash(), euValuation.ID, euValuation.ContextHash())
	}
	input.EffectiveQualifiers = []metering.Dimension{{Name: "region", Value: "us"}, {Name: "tier", Value: "standard"}}
	input.InputSetHash = ""
	reordered, err := rater.Rate(context.Background(), input)
	if err != nil {
		t.Fatalf("reordered qualifier rate: %v", err)
	}
	if reordered.ID != usValuation.ID || reordered.ContextHash() != usValuation.ContextHash() {
		t.Fatalf("qualifier ordering changed identity: US=%q/%q reordered=%q/%q", usValuation.ID, usValuation.ContextHash(), reordered.ID, reordered.ContextHash())
	}
}

func lineForRepair6Key(valuation economics.Valuation, key metering.ComponentKey) *economics.LineItem {
	for i := range valuation.Lines {
		if valuation.Lines[i].Component != nil && valuation.Lines[i].Component.CanonicalKey() == key.CanonicalKey() {
			return &valuation.Lines[i]
		}
	}
	return nil
}

func observationIDs(observations []metering.Observation) string {
	ids := make([]string, 0, len(observations))
	for _, observation := range observations {
		ids = append(ids, observation.ID)
	}
	return strings.Join(ids, ",")
}
