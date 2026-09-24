package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestPhase9Repair3_FixedOnlyUnavailableEvidenceCannotClaimComplete(t *testing.T) {
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	tariff := phase9Tariff(t, []economics.RatingRule{phase9FixedRule("call-fee", economics.FixedFeeScopeCall, "1")})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "fixed-only-unavailable", metering.OriginLocal, key, "1")
	observation.Authority = metering.AuthorityUnavailableClaim
	observation.Measures = nil
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff))
	if !errors.Is(err, ErrRatingEvidenceMissing) {
		t.Fatalf("unavailable evidence error=%v, want %v", err, ErrRatingEvidenceMissing)
	}
	if got.Completeness == economics.CompletenessComplete {
		t.Fatalf("fixed-only unavailable valuation=%+v, must not claim complete", got)
	}
	if len(got.Lines) != 1 || got.Lines[0].FixedFee == nil {
		t.Fatalf("independent fixed fee was not preserved: %+v", got.Lines)
	}
}

func TestPhase9Repair3_FixedFeeQualifierFailureCannotClaimComplete(t *testing.T) {
	key := phase9Key(metering.DirectionInput, metering.ComponentImage, metering.UnitImage)
	conditional := phase9FixedRule("conditional-fee", economics.FixedFeeScopeCall, "2")
	conditional.Conditions = []economics.QualifierCondition{{Name: "plan", Value: "pro"}}
	tariff := phase9Tariff(t, []economics.RatingRule{
		phase9LinearRule("image", key, "1"),
		phase9FixedRule("base-fee", economics.FixedFeeScopeCall, "1"),
		conditional,
	})
	rater, err := NewReferenceRater(tariff)
	if err != nil {
		t.Fatalf("NewReferenceRater: %v", err)
	}
	observation := phase9Observation(t, "fixed-missing-qualifier", metering.OriginLocal, key, "1")
	got, err := rater.Rate(context.Background(), phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}, tariff))
	if !errors.Is(err, ErrQualifierMissing) {
		t.Fatalf("missing qualifier error=%v, want %v", err, ErrQualifierMissing)
	}
	if got.Completeness == economics.CompletenessComplete {
		t.Fatalf("missing qualifier valuation=%+v, must not claim complete", got)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("independently chargeable lines were not preserved: %+v", got.Lines)
	}
}
