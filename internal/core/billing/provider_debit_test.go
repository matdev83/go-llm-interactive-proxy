package billing

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestProviderDebitDoesNotEnterIndependentMoneyRatingPlanes(t *testing.T) {
	key := metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit, SchemaID: "provider-debit.v1"}
	observation := phase9Observation(t, "provider-debit-only", metering.OriginProvider, key, "3")
	observation.Subject.Kind = metering.SubjectProviderDebit
	observation.Subject.RequestID = "request-provider-debit"
	observation.Subject.ProviderAccountKey = "provider-account"
	observation.Subject.PoolID = "pool"
	observation.Subject.WindowID = "window"
	observation.Subject.ResetAt = time.Unix(100, 0).UTC()
	observation.Correlation.RequestID = observation.Subject.RequestID
	observation.Correlation.ProviderAccountKey = observation.Subject.ProviderAccountKey
	if err := observation.Validate(); err != nil {
		t.Fatalf("provider debit observation: %v", err)
	}

	rater := &providerDebitCountingRater{}
	valuations, err := RateIndependentValuations(context.Background(), rater, phase9RatingInput(t, economics.BasisLocalExpected, []metering.Observation{observation}))
	if err != nil {
		t.Fatalf("RateIndependentValuations: %v", err)
	}
	if rater.calls != 0 {
		t.Fatalf("provider debit reached a money rater %d times", rater.calls)
	}
	if valuations.Expected != nil || valuations.ProviderQuantity != nil || valuations.ProviderReported != nil {
		t.Fatalf("provider debit entered an independent valuation plane: %+v", valuations)
	}
}

type providerDebitCountingRater struct{ calls int }

func (r *providerDebitCountingRater) Rate(context.Context, economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.calls++
	return economics.Valuation{}, nil
}
