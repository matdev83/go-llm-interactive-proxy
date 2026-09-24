package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair2_ReplayReceiptMetadataDoesNotChangeRatingIdentity(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	base := phase9Observation(t, "receipt-base", metering.OriginLocal, key, "2")
	base.ReceivedAt = time.Unix(100, 0).UTC()
	retry := base.Clone()
	retry.ReceivedAt = time.Unix(200, 0).UTC()
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := phase9Observation(t, "receipt-correction", metering.OriginLocal, key, "1")
	correction.Sequence = 2
	correction.ReceivedAt = time.Unix(300, 0).UTC()
	correction.Semantics = metering.SemanticsCorrection
	correction.Supersedes = []metering.ObservationRef{baseRef}

	var first economics.Valuation
	for i, observations := range [][]metering.Observation{
		{correction, base, retry},
		{retry, correction, base},
	} {
		input := phase9RatingInput(t, economics.BasisLocalExpected, observations, tariff)
		got, rateErr := rater.Rate(context.Background(), input)
		if rateErr != nil {
			t.Fatalf("delivery %d rating: %v", i, rateErr)
		}
		if i == 0 {
			first = got
			continue
		}
		if got.InputSetHash != first.InputSetHash || got.ID != first.ID {
			t.Fatalf("receipt retry changed identity: first=%q/%q got=%q/%q", first.InputSetHash, first.ID, got.InputSetHash, got.ID)
		}
		if len(got.InputObservations) != 2 || len(got.Lines) != 1 || len(got.Lines[0].SourceObservationRefs) != 2 {
			t.Fatalf("canonical refs=%+v lines=%+v, want one replay plus genuine correction refs", got.InputObservations, got.Lines)
		}
		for j := range first.InputObservations {
			if !first.InputObservations[j].Equal(got.InputObservations[j]) {
				t.Fatalf("receipt retry changed ref[%d]: first=%+v got=%+v", j, first.InputObservations[j], got.InputObservations[j])
			}
		}
	}
}

func TestPhase9Repair2_CorrectionOverUnusablePredecessorIsIncomplete(t *testing.T) {
	t.Parallel()
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9LinearRule("image", key, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	for _, tc := range []struct {
		name     string
		unusable func(*metering.Observation)
	}{
		{
			name: "authority unavailable",
			unusable: func(observation *metering.Observation) {
				observation.Authority = metering.AuthorityUnavailableClaim
				observation.Measures[0].Value = nil
				observation.Measures[0].Quality = metering.QualityUnavailable
			},
		},
		{
			name: "unknown quality",
			unusable: func(observation *metering.Observation) {
				observation.Measures[0].Quality = metering.QualityUnknown
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			predecessor := phase9Observation(t, "unusable-"+tc.name, metering.OriginLocal, key, "2")
			tc.unusable(&predecessor)
			predecessorRef, refErr := predecessor.Ref(predecessor.Subject.StoreID)
			if refErr != nil {
				t.Fatalf("predecessor ref: %v", refErr)
			}
			correction := phase9Observation(t, "correction-"+tc.name, metering.OriginLocal, key, "1")
			correction.Sequence = 2
			correction.Semantics = metering.SemanticsCorrection
			correction.Supersedes = []metering.ObservationRef{predecessorRef}
			input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{correction, predecessor}, tariff)
			got, rateErr := rater.Rate(context.Background(), input)
			if !errors.Is(rateErr, ErrRatingEvidenceMissing) {
				t.Fatalf("correction error=%v, want %v", rateErr, ErrRatingEvidenceMissing)
			}
			if got.Completeness == economics.CompletenessComplete || len(got.Totals) != 0 {
				t.Fatalf("correction valuation=%+v, want incomplete and unchargeable", got)
			}
		})
	}
}

func TestPhase9Repair2_WholeContextTierWaitsForCompleteSiblings(t *testing.T) {
	t.Parallel()
	uncached := phase9Key(metering.DirectionInput, metering.ComponentInputTokenUncached, metering.UnitToken)
	cached := phase9Key(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken)
	tariff := phase9Tariff(t, []economics.RatingRule{
		{
			ID: "uncached-whole", Component: &uncached, Currency: "USD", SelectionScope: economics.SelectionWholeContext,
			TierMode: economics.TierAllUnits,
			Tiers:    []economics.RatingTier{{UpTo: phase9Decimal("5"), UnitPrice: phase9Decimal("1")}, {UnitPrice: phase9Decimal("2")}},
		},
		phase9LinearRule("cached", cached, "1"),
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	available := phase9Observation(t, "whole-available", metering.OriginLocal, uncached, "4")
	unavailable := phase9Observation(t, "whole-unavailable", metering.OriginLocal, cached, "3")
	unavailable.Measures[0].Value = nil
	unavailable.Measures[0].Quality = metering.QualityUnavailable
	input := phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{available, unavailable}, tariff)
	got, rateErr := rater.Rate(context.Background(), input)
	if !errors.Is(rateErr, ErrQuantityIncomplete) {
		t.Fatalf("whole-context error=%v, want %v", rateErr, ErrQuantityIncomplete)
	}
	for _, line := range got.Lines {
		if line.Status == economics.RatingLineRated || line.Status == economics.RatingLineExplicitFree {
			t.Fatalf("incomplete context produced rated monetary line: %+v", line)
		}
	}
}
