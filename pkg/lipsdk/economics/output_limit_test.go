package economics_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type fakeLimitQuoter struct {
	last economics.OutputLimitRequest
}

func (f *fakeLimitQuoter) Rate(_ context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	return economics.RatingResult{
		Money:       economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		Perspective: req.Perspective,
		RaterID:     "fake-limit",
	}, nil
}

func (f *fakeLimitQuoter) QuoteOutputLimit(_ context.Context, req economics.OutputLimitRequest) (economics.OutputLimitResult, error) {
	f.last = req
	return economics.OutputLimitResult{
		Status:          economics.OutputLimitOK,
		MaxOutputTokens: 2_048,
		Source:          "fake-limit",
		Authority:       "estimated",
		Version:         economics.VersionRef{ID: "pb", Version: "v1"},
		EffectiveAt:     time.Unix(10, 0).UTC(),
		RoundingPolicy:  economics.RoundingTowardZero,
		Perspective:     req.Perspective,
		RaterID:         "fake-limit",
	}, nil
}

func TestOutputLimitQuoter_PublicContract(t *testing.T) {
	t.Parallel()
	var r economics.Rater = &fakeLimitQuoter{}
	var q economics.OutputLimitQuoter = &fakeLimitQuoter{}
	_ = r
	got, err := q.QuoteOutputLimit(context.Background(), economics.OutputLimitRequest{
		Perspective: metering.PerspectiveOperator,
		BackendID:   "b",
		Model:       "m",
		FixedQuantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     100,
			Present:   true,
		}},
		MaxMoney: economics.Money{NanoUnits: 5_000_000_000, Currency: "USD", Present: true},
		At:       time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != economics.OutputLimitOK || got.MaxOutputTokens != 2_048 {
		t.Fatalf("%#v", got)
	}
	if got.Perspective != metering.PerspectiveOperator || got.RaterID == "" || got.Version.Version == "" {
		t.Fatalf("provenance incomplete: %#v", got)
	}
	for _, st := range []economics.OutputLimitStatus{
		economics.OutputLimitOK,
		economics.OutputLimitCapacityExhausted,
		economics.OutputLimitUnsupported,
		economics.OutputLimitOverflow,
	} {
		if st == "" {
			t.Fatal("status constants must be non-empty")
		}
	}
}
