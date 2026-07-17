package economics_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type stubRater struct{}

func (stubRater) Rate(context.Context, economics.RatingRequest) (economics.RatingResult, error) {
	return economics.RatingResult{
		Money:       economics.Money{Present: true, NanoUnits: 1, Currency: "USD"},
		Perspective: metering.PerspectiveOperator,
		RaterID:     "stub",
		Version:     economics.VersionRef{ID: "stub", Version: "v1"},
	}, nil
}

func TestCompileContract_RaterRegistration(t *testing.T) {
	t.Parallel()
	reg := economics.RaterRegistration{
		ID:          "operator-rater",
		Perspective: metering.PerspectiveOperator,
		Rater:       stubRater{},
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
}

func TestRaterRegistration_RejectsNilRater(t *testing.T) {
	t.Parallel()
	reg := economics.RaterRegistration{
		ID:          "operator-rater",
		Perspective: metering.PerspectiveOperator,
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("nil rater must be rejected")
	}
}

func TestRaterRegistration_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	reg := economics.RaterRegistration{
		ID:          "  ",
		Perspective: metering.PerspectiveOperator,
		Rater:       stubRater{},
	}
	err := reg.Validate()
	if err == nil {
		t.Fatal("whitespace rater id must be rejected")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Fatalf("err=%v", err)
	}
}

func TestRaterRegistration_RejectsUnknownPerspective(t *testing.T) {
	t.Parallel()
	reg := economics.RaterRegistration{
		ID:          "bad",
		Perspective: metering.EconomicPerspective("nope"),
		Rater:       stubRater{},
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("unknown perspective must be rejected")
	}
}

func TestRaterRegistration_RejectsTypedNilRater(t *testing.T) {
	t.Parallel()
	var typedNil *ptrRater
	reg := economics.RaterRegistration{
		ID:          "operator-rater",
		Perspective: metering.PerspectiveOperator,
		Rater:       typedNil,
	}
	if err := reg.Validate(); err == nil {
		t.Fatal("typed-nil rater must be rejected")
	}
}

type ptrRater struct{}

func (p *ptrRater) Rate(ctx context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	return stubRater{}.Rate(ctx, req)
}
