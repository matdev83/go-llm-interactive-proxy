package billing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair5_MixedAuthorityPartitionsRemainIndependent(t *testing.T) {
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
	observation := phase9Observation(t, "mixed-authority", metering.OriginLocal, imageKey, "2")
	observation.Authority = metering.AuthorityUnavailableClaim
	observation.Measures = append(observation.Measures, metering.Measure{
		Key: audioKey, Quality: metering.QualityUnavailable,
	})
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff)
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrQuantityIncomplete) && !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want authority evidence diagnostic", rateErr)
	}
	var imageLine, audioLine *economics.LineItem
	for i := range got.Lines {
		line := &got.Lines[i]
		switch line.Component.CanonicalKey() {
		case imageKey.CanonicalKey():
			imageLine = line
		case audioKey.CanonicalKey():
			audioLine = line
		}
	}
	if imageLine == nil || imageLine.Status != economics.RatingLineRated || imageLine.Amount == nil || imageLine.Amount.CanonicalString() != "2/0" {
		t.Fatalf("healthy image line=%+v, want independently rated amount 2", imageLine)
	}
	if audioLine == nil || audioLine.Status != economics.RatingLineQuantityIncomplete {
		t.Fatalf("affected audio line=%+v, want quantity-incomplete partition", audioLine)
	}
	if len(got.MissingObservations) == 0 {
		t.Fatalf("valuation=%+v, want retained authority diagnostic", got)
	}
}

func TestPhase9Repair5_MixedCorrectionFieldsRemainIndependent(t *testing.T) {
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
	base := phase9Observation(t, "mixed-correction-base", metering.OriginLocal, imageKey, "2")
	base.Semantics = metering.SemanticsCumulative
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase9Observation(t, "mixed-correction", metering.OriginLocal, imageKey, "1")
	correction.Sequence = 2
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Measures = append(correction.Measures, metering.Measure{
		Key: audioKey, Value: phase9Decimal("1"), Quality: metering.QualityObserved,
	})
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{correction, base}, tariff)
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want unusable-field diagnostic", rateErr)
	}
	var imageLine, audioLine *economics.LineItem
	for i := range got.Lines {
		line := &got.Lines[i]
		if line.Component == nil {
			continue
		}
		switch line.Component.CanonicalKey() {
		case imageKey.CanonicalKey():
			imageLine = line
		case audioKey.CanonicalKey():
			audioLine = line
		}
	}
	if imageLine == nil || imageLine.Status != economics.RatingLineRated || imageLine.Amount == nil || imageLine.Amount.CanonicalString() != "3/0" {
		t.Fatalf("healthy corrected image line=%+v, want amount 3", imageLine)
	}
	if audioLine == nil || audioLine.Status != economics.RatingLineQuantityIncomplete {
		t.Fatalf("affected corrected audio line=%+v, want quantity-incomplete partition", audioLine)
	}
	if len(got.MissingObservations) == 0 {
		t.Fatalf("valuation=%+v, want correction audit diagnostic", got)
	}
}

func TestPhase9Repair5_MixedPendingCorrectionFieldsRemainIndependent(t *testing.T) {
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
	base := phase9Observation(t, "mixed-pending-base", metering.OriginLocal, imageKey, "2")
	base.Semantics = metering.SemanticsCumulative
	base.Sequence = 1
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	pendingRef := metering.ObservationRef{
		StoreID: "store", ObservationID: "mixed-pending-audio", Revision: 1,
		PayloadHash: strings.Repeat("e", 64),
	}
	correction := phase9Observation(t, "mixed-pending-correction", metering.OriginLocal, imageKey, "1")
	correction.Sequence = 2
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef, pendingRef}
	correction.Measures = append(correction.Measures, metering.Measure{
		Key: audioKey, Value: phase9Decimal("1"), Quality: metering.QualityObserved,
	})
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{correction, base}, tariff)
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want pending evidence diagnostic", rateErr)
	}
	var imageLine, audioLine *economics.LineItem
	for i := range got.Lines {
		line := &got.Lines[i]
		if line.Component == nil {
			continue
		}
		switch line.Component.CanonicalKey() {
		case imageKey.CanonicalKey():
			imageLine = line
		case audioKey.CanonicalKey():
			audioLine = line
		}
	}
	if imageLine == nil || imageLine.Status != economics.RatingLineRated || imageLine.Amount == nil || imageLine.Amount.CanonicalString() != "3/0" {
		t.Fatalf("healthy pending-correction image line=%+v, want amount 3", imageLine)
	}
	if audioLine == nil || audioLine.Status != economics.RatingLineQuantityIncomplete {
		t.Fatalf("affected pending-correction audio line=%+v, want quantity-incomplete partition", audioLine)
	}
	if len(got.MissingObservations) == 0 {
		t.Fatalf("valuation=%+v, want pending correction audit diagnostic", got)
	}
}

func TestPhase9Repair5_MixedProviderChargesKeepHealthyChargeRateable(t *testing.T) {
	t.Parallel()
	healthyKey := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	affectedKey := phase9Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	observation := phase9Observation(t, "mixed-provider-charges", metering.OriginProvider, healthyKey, "1")
	observation.Measures = nil
	observation.Charges = []metering.ReportedCharge{
		{ChargeItemID: "healthy-charge", Component: &healthyKey, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent},
		{
			ChargeItemID: "affected-charge", Component: &affectedKey, Amount: phase9Decimal("9"), Currency: "USD", Kind: metering.ChargeKindComponent,
			Covers: []metering.ChargeCoverageRef{{
				Ref:      metering.ChargeRef{StoreID: "store", ObservationID: "late-aggregate", Revision: 1, ChargeItemID: "aggregate"},
				Relation: metering.CoverageInclusive,
			}},
		},
	}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{observation})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want pending coverage diagnostic", rateErr)
	}
	if len(got.Lines) != 1 || got.Lines[0].ItemID != "healthy-charge" || got.Lines[0].Status != economics.RatingLineProviderReported {
		t.Fatalf("valuation lines=%+v, want only healthy provider charge retained", got.Lines)
	}
	if got.Lines[0].Amount == nil || got.Lines[0].Amount.CanonicalString() != "7/0" {
		t.Fatalf("healthy provider line=%+v, want amount 7", got.Lines[0])
	}
	if len(got.CoverageRefs) == 0 {
		t.Fatalf("valuation=%+v, want coverage diagnostic", got)
	}
}

func TestPhase9Repair5_MixedProviderCorrectionChargesRemainIndependent(t *testing.T) {
	t.Parallel()
	healthyKey := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	affectedKey := phase9Key(metering.DirectionInput, metering.ComponentAudio, metering.UnitSecond)
	base := phase9Observation(t, "mixed-provider-correction-base", metering.OriginProvider, healthyKey, "1")
	base.Measures = nil
	base.Semantics = metering.SemanticsCumulative
	base.Charges = []metering.ReportedCharge{{
		ChargeItemID: "healthy-charge", Component: &healthyKey, Amount: phase9Decimal("7"), Currency: "USD", Kind: metering.ChargeKindComponent,
	}}
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase9Observation(t, "mixed-provider-correction", metering.OriginProvider, healthyKey, "1")
	correction.Measures = nil
	correction.Sequence = 2
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef}
	correction.Charges = []metering.ReportedCharge{
		{ChargeItemID: "healthy-charge", Component: &healthyKey, Amount: phase9Decimal("8"), Currency: "USD", Kind: metering.ChargeKindComponent},
		{ChargeItemID: "affected-charge", Component: &affectedKey, Amount: phase9Decimal("9"), Currency: "USD", Kind: metering.ChargeKindComponent},
	}
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{correction, base})
	input.Tariff = economics.RatingSnapshotRef{}
	input.TariffContent = nil
	input.RaterContent = nil
	got, rateErr := RateProviderReported(context.Background(), input)
	if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
		t.Fatalf("rating error=%v, want unusable charge diagnostic", rateErr)
	}
	if len(got.Lines) != 1 || got.Lines[0].ItemID != "healthy-charge" || got.Lines[0].Status != economics.RatingLineProviderReported {
		t.Fatalf("valuation lines=%+v, want only healthy corrected charge retained", got.Lines)
	}
	if got.Lines[0].Amount == nil || got.Lines[0].Amount.CanonicalString() != "8/0" {
		t.Fatalf("healthy corrected provider line=%+v, want amount 8", got.Lines[0])
	}
	if len(got.MissingObservations) == 0 {
		t.Fatalf("valuation=%+v, want correction audit diagnostic", got)
	}
}
