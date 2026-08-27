package runtimebundle

import (
	"context"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type compressionMergeTerminalProvider struct{ id string }

func (p compressionMergeTerminalProvider) ID() string { return p.id }

func (compressionMergeTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

func TestBindReasoningCompression_RejectsInvalidInputBeforePublication(t *testing.T) {
	t.Parallel()
	gen := featurebundle.GeneratedMergeSurface{}
	got, err := gen.BindAttemptTransforms("test-plugin", []request.AttemptTransform{nil})
	if err == nil {
		t.Fatal("nil attempt transform was accepted")
	}
	if !reflect.DeepEqual(got, featurebundle.GeneratedMergeSurface{}) {
		t.Fatalf("failed composition returned a non-empty surface: %#v", got)
	}
}
