package runtimebundle

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

type compressionMergeTerminalProvider struct{ id string }

func (p compressionMergeTerminalProvider) ID() string { return p.id }

func (compressionMergeTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

func TestAppendReasoningCompressionBundle_RejectsProviderConflictBeforePublication(t *testing.T) {
	t.Parallel()
	first := compressionMergeTerminalProvider{id: "compression.first"}
	second := compressionMergeTerminalProvider{id: "compression.second"}
	got, err := appendReasoningCompressionBundle(
		featurebundle.MergedFeatureSurface{TerminalDecisionProvider: first},
		lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: second,
		},
	)
	if err == nil {
		t.Fatal("conflicting compression contribution was accepted")
	}
	if !errors.Is(err, featurebundle.ErrTerminalDecisionProviderConflict) {
		t.Fatalf("error = %v, want terminal-decision provider conflict", err)
	}
	if !reflect.DeepEqual(got, featurebundle.MergedFeatureSurface{}) {
		t.Fatalf("failed composition returned a partial surface: %#v", got)
	}
}
