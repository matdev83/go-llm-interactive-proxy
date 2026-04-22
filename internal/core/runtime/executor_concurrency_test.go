package runtime_test

import (
	"context"
	"math/rand"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
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
		Backends: map[string]runtime.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
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
		Rand: rand.New(rand.NewSource(99)),
	}

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
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
		}()
	}
	wg.Wait()
}

// TestExecutor_concurrentExecute_sharedRand_weighted exercises a single *math/rand.Rand
// across concurrent Execute calls while weighted routing calls Intn. Without
// serialization this is a data race (see math/rand.Rand docs). Verified under
// go test -race on platforms where the race runtime can be initialized.
func TestExecutor_concurrentExecute_sharedRand_weighted(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewSource(17))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
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
		Rand:  r,
		Backends: map[string]runtime.Backend{
			"a": {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
			"b": {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
		},
	}
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	sel := "[weight=1]a:m1^[weight=1]b:m2"
	for range n {
		go func() {
			defer wg.Done()
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
		}()
	}
	wg.Wait()
}
