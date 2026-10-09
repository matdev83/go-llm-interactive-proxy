package runtime_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

func TestDecisionSecretGuard_DeniesBeforeGuardOrUpstream(t *testing.T) {
	t.Parallel()
	for _, guarded := range []bool{true, false} {
		t.Run(map[bool]string{true: "guarded", false: "unguarded"}[guarded], func(t *testing.T) {
			t.Parallel()
			var evaluations, opens atomic.Int32
			var guards []secretguard.Guard
			if guarded {
				guards = []secretguard.Guard{&blockingSecretGuard{evals: &evaluations}}
			}
			ex := runtime.TestExecutor()
			var err error
			ex.Store, err = b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
				FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{SecretGuards: guards}),
			})
			ex.Bus = ex.RuntimeSnapshot.HookBus()
			ex.Backends = map[string]execbackend.Backend{"decision": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityDecisions),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventResponseFinished}}), nil
				},
			}}
			call := &lipapi.Call{
				Route:      lipapi.RouteIntent{Selector: "decision:jev"},
				Decision:   &lipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []lipapi.DecisionQuestion{{ID: "q", Kind: lipapi.DecisionKindNoul}}},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationDecisionEvaluate, DeliveryMode: lipapi.DeliveryModeNonStreaming},
			}
			stream, err := ex.Execute(t.Context(), call)
			if stream != nil {
				defer func() { _ = stream.Close() }()
			}
			if guarded {
				if !lipapi.IsPolicyDenied(err) || evaluations.Load() != 0 || opens.Load() != 0 {
					t.Fatalf("guarded decision: err=%v evaluations=%d opens=%d", err, evaluations.Load(), opens.Load())
				}
			} else if err != nil || opens.Load() != 1 {
				t.Fatalf("unguarded decision: err=%v opens=%d", err, opens.Load())
			}
		})
	}
}
