package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// Post-hook exclusion of a [first] candidate must not persist WeightedFirstConsumed.
func TestWeightedFirst_postHookExcludeDoesNotConsumeStoreFlag(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var openedA, openedB atomic.Int64
	var openedModel string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.MaxAttempts = 4
	ex.Rand = routing.NewSeededRng(3)
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{postHookMutator{mode: "tools"}}})
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openedA.Add(1)
				return nil, lipapi.RecoverablePreOutputError(context.Canceled)
			},
		},
		"b": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, _ lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openedB.Add(1)
				openedModel = cand.Primary.Backend + ":" + cand.Primary.Model
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := postHookBaseCall("[first]a:m^b:m")
	stream, execErr := ex.Execute(t.Context(), call)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}
	_, _ = lipapi.Collect(t.Context(), stream)
	if openedA.Load() != 0 {
		t.Fatalf("first branch must be excluded before Open, opensA=%d", openedA.Load())
	}
	if openedB.Load() != 1 || openedModel != "b:m" {
		t.Fatalf("want failover open b:m, got model=%q opensB=%d", openedModel, openedB.Load())
	}
	alegID := call.Session.ALegID
	if alegID == "" {
		t.Fatal("missing ALegID on call after execute")
	}
	leg, ferr := st.FetchALeg(t.Context(), alegID)
	if ferr != nil {
		t.Fatalf("FetchALeg: %v", ferr)
	}
	if leg.WeightedFirstConsumed {
		t.Fatal("WeightedFirstConsumed must stay false when [first] candidate never opened")
	}
}
