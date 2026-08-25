package terminaldecision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

type typedNilAuxiliaryClient struct{}

func (*typedNilAuxiliaryClient) Collect(context.Context, auxiliary.Request) (lipapi.Collected, error) {
	return lipapi.Collected{}, nil
}

func (*typedNilAuxiliaryClient) Stream(context.Context, auxiliary.Request) (lipapi.EventStream, error) {
	return nil, nil
}

func TestInput_AuxiliaryTypedNilFailsClosedWithoutPanic(t *testing.T) {
	t.Parallel()

	var client *typedNilAuxiliaryClient
	in := Input{
		Candidate: CanonicalTerminalCandidate{Cause: CandidateCauseNormal, Reference: "candidate"},
		Request:   RequestIdentity{RequestID: "request", TraceID: "trace", ALegID: "a-leg", BLegID: "b-leg"},
		Policy:    PolicySnapshot{Revision: "policy"},
		Auxiliary: client,
		Deadline:  time.Now().Add(time.Minute),
	}
	if err := in.Validate(); err == nil {
		t.Fatal("typed-nil auxiliary client must fail closed during input validation")
	} else if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("typed-nil auxiliary error=%v, want ErrInvalidInput", err)
	}
}
