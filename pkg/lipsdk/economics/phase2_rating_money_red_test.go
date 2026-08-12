package economics_test

import (
	"math"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 2.1 RED economics contracts (requirements 4.6–4.10, 13.1, 13.3;
// design D5, D14, D17).
//
// Deferred to later phases where needed: versioned Convert seam, public RateSafe
// invoker. Money/currency coherence is asserted via designed RatingResult.ValidateFor,
// Money.Validate, NanoRate presence, RoundToInt64, and existing Add/Mul checked ops.

func TestPhase2_CompileContract_RatingResultValidateFor(t *testing.T) {
	t.Parallel()
	type ratingValidateFor interface {
		ValidateFor(economics.RatingRequest) error
	}
	if _, ok := any(economics.RatingResult{}).(ratingValidateFor); !ok {
		t.Fatal("RatingResult.ValidateFor(RatingRequest) missing (design External Result Validation; task 2.4)")
	}
}

func TestPhase2_Money_AbsentVsAuthoritativeZero(t *testing.T) {
	t.Parallel()
	absent := economics.Money{}
	zero := economics.Money{NanoUnits: 0, Currency: "USD", Present: true}
	if absent.Present == zero.Present {
		t.Fatal("absent and authoritative zero must remain distinct (req 4.8)")
	}
}

func TestPhase2_Money_MixedCurrencyAggregationRejected(t *testing.T) {
	t.Parallel()
	usd := economics.Money{NanoUnits: 10, Currency: "USD", Present: true}
	eur := economics.Money{NanoUnits: 5, Currency: "EUR", Present: true}
	if _, err := usd.Add(eur); err == nil {
		t.Fatal("mixed-currency Add must fail (req 4.9)")
	}
}

func TestPhase2_Money_OverflowSpendRejected(t *testing.T) {
	t.Parallel()
	maxMoney := economics.Money{NanoUnits: math.MaxInt64, Currency: "USD", Present: true}
	one := economics.Money{NanoUnits: 1, Currency: "USD", Present: true}
	if _, err := maxMoney.Add(one); err == nil {
		t.Fatal("overflow Add must fail (req 4.6)")
	}
	if _, err := economics.MulTokensByRatePer1M(math.MaxInt64, math.MaxInt64); err == nil {
		t.Fatal("overflow MulTokensByRatePer1M must fail (req 4.6, 4.10)")
	}
}

func TestPhase2_Money_EmptyPresentCurrencyRejectedByAdd(t *testing.T) {
	t.Parallel()
	a := economics.Money{NanoUnits: 1, Currency: "", Present: true}
	b := economics.Money{NanoUnits: 1, Currency: "USD", Present: true}
	if _, err := a.Add(b); err == nil {
		t.Fatal("empty present currency must fail checked Add (req 4.6)")
	}
}

func TestPhase2_RatingResultValidateFor_RejectsPerspectiveMismatch(t *testing.T) {
	t.Parallel()
	type ratingValidateFor interface {
		ValidateFor(economics.RatingRequest) error
	}
	req := economics.RatingRequest{Perspective: metering.PerspectiveCustomer}
	res := economics.RatingResult{
		Money:       economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		Perspective: metering.PerspectiveOperator,
		RaterID:     "ref",
		Version:     economics.VersionRef{ID: "pb", Version: "1"},
	}
	v, ok := any(res).(ratingValidateFor)
	if !ok {
		t.Fatal("RatingResult.ValidateFor missing (req 4.7; task 2.4)")
	}
	if err := v.ValidateFor(req); err == nil {
		t.Fatal("perspective mismatch must fail (req 4.7)")
	}
}

func TestPhase2_RatingResultValidateFor_AcceptsAuthoritativeZero(t *testing.T) {
	t.Parallel()
	req := economics.RatingRequest{
		Perspective: metering.PerspectiveOperator,
		FactIDs:     []string{"fact-1"},
		At:          time.Unix(10, 0).UTC(),
	}
	res := economics.RatingResult{
		Money:       economics.Money{NanoUnits: 0, Currency: "USD", Present: true},
		Perspective: metering.PerspectiveOperator,
		RaterID:     "ref",
		Version:     economics.VersionRef{ID: "pb", Version: "1"},
	}
	if err := res.ValidateFor(req); err != nil {
		t.Fatalf("authoritative zero must validate: %v", err)
	}
}

func TestPhase2_RatingResultValidateFor_RejectsMissingRaterVersionEvidence(t *testing.T) {
	t.Parallel()
	type ratingValidateFor interface {
		ValidateFor(economics.RatingRequest) error
	}
	req := economics.RatingRequest{
		Perspective: metering.PerspectiveOperator,
		FactIDs:     []string{"fact-1"},
		At:          time.Unix(10, 0).UTC(),
	}
	cases := []struct {
		name string
		res  economics.RatingResult
	}{
		{
			name: "empty_rater",
			res: economics.RatingResult{
				Money:       economics.Money{NanoUnits: 0, Currency: "USD", Present: true},
				Perspective: metering.PerspectiveOperator,
				Version:     economics.VersionRef{ID: "pb", Version: "1"},
			},
		},
		{
			name: "empty_version",
			res: economics.RatingResult{
				Money:       economics.Money{NanoUnits: 0, Currency: "USD", Present: true},
				Perspective: metering.PerspectiveOperator,
				RaterID:     "ref",
			},
		},
		{
			name: "absent_money_as_authoritative",
			res: economics.RatingResult{
				Money:       economics.Money{},
				Perspective: metering.PerspectiveOperator,
				RaterID:     "ref",
				Version:     economics.VersionRef{ID: "pb", Version: "1"},
			},
		},
		{
			name: "negative_money",
			res: economics.RatingResult{
				Money:       economics.Money{NanoUnits: -1, Currency: "USD", Present: true},
				Perspective: metering.PerspectiveOperator,
				RaterID:     "ref",
				Version:     economics.VersionRef{ID: "pb", Version: "1"},
			},
		},
		{
			name: "empty_currency",
			res: economics.RatingResult{
				Money:       economics.Money{NanoUnits: 1, Currency: "", Present: true},
				Perspective: metering.PerspectiveOperator,
				RaterID:     "ref",
				Version:     economics.VersionRef{ID: "pb", Version: "1"},
			},
		},
		{
			name: "unnormalized_currency",
			res: economics.RatingResult{
				Money:       economics.Money{NanoUnits: 1, Currency: "usd", Present: true},
				Perspective: metering.PerspectiveOperator,
				RaterID:     "ref",
				Version:     economics.VersionRef{ID: "pb", Version: "1"},
			},
		},
		{
			name: "unknown_rounding",
			res: economics.RatingResult{
				Money:          economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
				Perspective:    metering.PerspectiveOperator,
				RaterID:        "ref",
				Version:        economics.VersionRef{ID: "pb", Version: "1"},
				RoundingPolicy: economics.RoundingPolicy("bankers_unknown"),
			},
		},
		{
			name: "invalid_line_id",
			res: economics.RatingResult{
				Money:       economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
				Perspective: metering.PerspectiveOperator,
				RaterID:     "ref",
				Version:     economics.VersionRef{ID: "pb", Version: "1"},
				LineID:      "line\x00leak",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, ok := any(tc.res).(ratingValidateFor)
			if !ok {
				t.Fatal("RatingResult.ValidateFor missing (req 4.7; task 2.4)")
			}
			if err := v.ValidateFor(req); err == nil {
				t.Fatal("invalid rating result must fail ValidateFor (req 4.6–4.8, 4.10)")
			}
		})
	}
}
