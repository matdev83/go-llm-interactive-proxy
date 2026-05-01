package extensions_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
)

type benchPol struct{}

func (benchPol) ID() string                        { return "bench-pol" }
func (benchPol) Order() int                        { return 0 }
func (benchPol) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (benchPol) Handle(context.Context, lipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

// BenchmarkRunToolPolicyStage_preSortedPolicies measures the steady-state tool policy stage when
// policies are already in execution order (runtime path).
func BenchmarkRunToolPolicyStage_preSortedPolicies(b *testing.B) {
	ctx := context.Background()
	ev := lipapi.ToolEvent{Kind: lipapi.ToolEventStarted, ToolCallID: "tc1", ToolName: "fn"}
	policies := toolpolicy.MaterializeSorted([]toolpolicy.Policy{benchPol{}})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
			Ctx:      ctx,
			Policies: policies,
			Event:    ev,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
