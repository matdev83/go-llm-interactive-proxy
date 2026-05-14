package runtime_test

import (
	"context"
	randv2 "math/rand/v2"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// TestExecutor_concurrentExecute_sharedEmptyHooksBus stresses a shared Executor whose hook bus is
// the explicit empty-bus value from hooks.New — there must be no data race on Executor.Bus.
func TestExecutor_concurrentExecute_sharedEmptyHooksBus(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					_ = ctx
					_ = call
					_ = cand
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
		Rand: routing.WrapRandV2(randv2.New(randv2.NewPCG(99, 0))),
	}

	const n = 64
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			call := &lipapi.Call{
				Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []lipapi.Message{{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{lipapi.TextPart("hi")},
				}},
			}
			_, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Errorf("Execute: %v", err)
				return
			}
		})
	}
	wg.Wait()
}

// TestExecutor_concurrentExecute_sharedRand_weighted exercises a single *rand/v2.Rand
// across concurrent Execute calls while weighted routing calls Intn. Without
// serialization this is a data race (see math/rand/v2.Rand docs). Verified under
// go test -race on platforms where the race runtime can be initialized.
func TestExecutor_concurrentExecute_sharedRand_weighted(t *testing.T) {
	t.Parallel()
	r := randv2.New(randv2.NewPCG(17, 0))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		_ = ctx
		_ = call
		_ = cand
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	}
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.WrapRandV2(r),
		Backends: map[string]execbackend.Backend{
			"a": {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
			"b": {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
		},
	}
	const n = 64
	var wg sync.WaitGroup
	sel := "[weight=1]a:m1^[weight=1]b:m2"
	for range n {
		wg.Go(func() {
			call := &lipapi.Call{
				Route: lipapi.RouteIntent{Selector: sel},
				Messages: []lipapi.Message{{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{lipapi.TextPart("hi")},
				}},
			}
			_, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Errorf("Execute: %v", err)
			}
		})
	}
	wg.Wait()
}
