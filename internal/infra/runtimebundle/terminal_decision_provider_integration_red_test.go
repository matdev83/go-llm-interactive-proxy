package runtimebundle_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"gopkg.in/yaml.v3"
)

type terminalDecisionIntegrationProvider struct {
	id       string
	decideFn func(int32, terminaldecision.Input) terminaldecision.Decision
	calls    atomic.Int32
	inputs   chan terminaldecision.Input
}

func (p *terminalDecisionIntegrationProvider) ID() string { return p.id }

func (p *terminalDecisionIntegrationProvider) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	n := p.calls.Add(1)
	if p.inputs != nil {
		p.inputs <- in
	}
	return p.decideFn(n, in), nil
}

func TestTerminalDecisionProviderFeatureIntegrationAndRemoval(t *testing.T) {
	t.Parallel()

	t.Run("allow-stop reaches core terminal finalization", func(t *testing.T) {
		provider := &terminalDecisionIntegrationProvider{
			id: "terminal-integration.allow-stop",
			decideFn: func(_ int32, _ terminaldecision.Input) terminaldecision.Decision {
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "test-allow"}
			},
		}
		bundle := compileIntegrationProviderGeneration(t, provider)
		body := postResponses(t, bundle.Handler(), "stub-default")
		if got := provider.calls.Load(); got != 1 {
			t.Fatalf("provider calls = %d, want one terminal evaluation", got)
		}
		if body == "" {
			t.Fatal("allow-stop request returned an empty response")
		}
	})

	t.Run("continue invokes core continuation transaction and publishes B2", func(t *testing.T) {
		provider := &terminalDecisionIntegrationProvider{
			id:     "terminal-integration.continue",
			inputs: make(chan terminaldecision.Input, 2),
			decideFn: func(n int32, _ terminaldecision.Input) terminaldecision.Decision {
				if n == 1 {
					return terminaldecision.Decision{
						Kind:       terminaldecision.DecisionContinue,
						ReasonCode: "test-continue",
						Continue: &terminaldecision.ContinuationIntent{
							TrajectoryRef: "test-trajectory",
							ControlRef:    "test-control",
							Instruction:   "continue the in-scope test task",
							Provenance:    "internal-control",
							ReasonCode:    "test-continue",
						},
					}
				}
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "test-allow"}
			},
		}
		bundle := compileIntegrationProviderGeneration(t, provider)
		body := postResponses(t, bundle.Handler(), "stub-default")
		if got := provider.calls.Load(); got != 2 {
			t.Fatalf("provider calls = %d, want B1 and B2 terminal evaluations", got)
		}
		first, second := <-provider.inputs, <-provider.inputs
		if first.Request.BLegID == "" || first.Request.BLegID == second.Request.BLegID {
			t.Fatalf("continuation did not publish a distinct B2 leg: B1=%q B2=%q", first.Request.BLegID, second.Request.BLegID)
		}
		if body == "" {
			t.Fatal("continued request returned an empty response")
		}
	})

	t.Run("removing registration preserves no-provider behavior", func(t *testing.T) {
		provider := &terminalDecisionIntegrationProvider{
			id: "terminal-integration.removed",
			decideFn: func(_ int32, _ terminaldecision.Input) terminaldecision.Decision {
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}
			},
		}
		process := newProcessForGeneration(t)
		bundle := compileTerminalDecisionGeneration(t, process, terminalDecisionCandidate(t, "", false))
		t.Cleanup(func() { _ = bundle.Close() })
		body := postResponses(t, bundle.Handler(), "stub-default")
		if !strings.Contains(body, "terminal-decision-text") {
			t.Fatalf("no-provider behavior changed: response missing baseline text: %s", body)
		}
		if got := provider.calls.Load(); got != 0 {
			t.Fatalf("removed provider calls = %d, want zero", got)
		}
	})
}

func compileIntegrationProviderGeneration(t *testing.T, provider terminaldecision.Provider) runtimebundle.GenerationRuntime {
	t.Helper()
	registry := stdFactoryCatalog(t)
	factoryID := "terminal-decision-integration"
	if err := registry.RegisterFeature(factoryID, func(yaml.Node) (lipfeature.FeatureBundle, error) {
		return lipfeature.FeatureBundle{
			SchemaVersion:            lipfeature.SchemaVersionV1,
			TerminalDecisionProvider: provider,
		}, nil
	}); err != nil {
		t.Fatalf("RegisterFeature(%q): %v", factoryID, err)
	}
	process, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	bundle := compileTerminalDecisionGeneration(t, process, terminalDecisionCandidate(t, factoryID, true))
	t.Cleanup(func() { _ = bundle.Close() })
	return bundle
}
