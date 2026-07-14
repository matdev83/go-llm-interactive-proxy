package economics_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type fakeRater struct {
	last economics.RatingRequest
}

func (f *fakeRater) Rate(ctx context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	_ = ctx
	f.last = req
	return economics.RatingResult{
		Money:          economics.Money{NanoUnits: 42, Currency: "USD", Present: true},
		Source:         "catalog",
		Authority:      "authoritative",
		Version:        economics.VersionRef{ID: "pb", Version: "1"},
		EffectiveAt:    time.Unix(10, 0).UTC(),
		LineID:         "line-1",
		RoundingPolicy: economics.RoundingHalfAwayFromZero,
		Perspective:    req.Perspective,
	}, nil
}

func TestRater_SeparatePerspectives(t *testing.T) {
	t.Parallel()
	var r economics.Rater = &fakeRater{}
	customer, err := r.Rate(context.Background(), economics.RatingRequest{
		Perspective: metering.PerspectiveCustomer,
		BackendID:   "b",
		Model:       "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if customer.Perspective != metering.PerspectiveCustomer || !customer.Money.Present {
		t.Fatalf("%#v", customer)
	}
	operator, err := r.Rate(context.Background(), economics.RatingRequest{
		Perspective: metering.PerspectiveOperator,
		BackendID:   "b",
		Model:       "m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if operator.Perspective != metering.PerspectiveOperator {
		t.Fatal(operator.Perspective)
	}
}

func TestExposureAndVersionRefs(t *testing.T) {
	t.Parallel()
	exp := economics.ConservativeOutputAssumption{
		BoundKind:   economics.OutputBoundConfiguredDefault,
		TokenCount: 1024,
		PolicyID:   "unknown_output_policy",
		Present:    true,
	}
	if err := exp.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := economics.ConservativeOutputAssumption{BoundKind: "nope", Present: true}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid bound kind")
	}
	v := economics.VersionRef{ID: "rules", Version: "v3", EffectiveAt: time.Unix(1, 0).UTC()}
	if v.ID == "" || v.Version == "" {
		t.Fatal("version identity")
	}
	_ = economics.RatingSnapshotRef{VersionRef: v}
	_ = economics.PolicySnapshotRef{VersionRef: v}
	basis := economics.ExposureBasis{
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Output:      exp,
	}
	if err := basis.Validate(); err != nil {
		t.Fatal(err)
	}
}
