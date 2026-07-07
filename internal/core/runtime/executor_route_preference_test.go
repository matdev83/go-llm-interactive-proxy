package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestExecutor_execute_routePreferenceDrivesPlanner locks the planning-side effect of
// execctx.RouteCandidatePreferences at the Execute boundary (Phase 4 Task 4.1 gap).
// When the caller injects a preferred candidate key via context, the executor's planner
// must prefer that backend when expanding failover groups.
func TestExecutor_execute_routePreferenceDrivesPlanner(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var preferredOpens int32
	var otherOpens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"preferred": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&preferredOpens, 1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
			"other": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&otherOpens, 1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.NewSeededRng(1),
	}
	call := &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "preferred:m|other:m"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	}
	// Inject route preference for "preferred" backend.
	ctx := execctx.WithRouteCandidatePreferences(context.Background(), []string{"preferred"})
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if atomic.LoadInt32(&preferredOpens) == 0 {
		t.Fatal("expected preferred backend to be opened; route preference was not honored by the planner")
	}
	if atomic.LoadInt32(&otherOpens) > 0 {
		t.Fatal("expected other backend to NOT be opened when preference is honored on first attempt")
	}
}
