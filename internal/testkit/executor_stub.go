package testkit

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

func NewStubExecutorWithDeltas(t *testing.T, caps lipapi.BackendCaps, deltas []string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
	}
	for _, d := range deltas {
		events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d})
	}
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	return &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(42)),
		Backends: map[string]runtime.Backend{
			"stub": {
				Caps: caps,
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					if capture != nil {
						capture.Store("last", call)
					}
					_ = ctx
					_ = cand
					return lipapi.FixedEventStream(events), nil
				},
			},
		},
	}
}

func NewStubExecutor(t *testing.T, caps lipapi.BackendCaps, text string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	return &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(42)),
		Backends: map[string]runtime.Backend{
			"stub": {
				Caps: caps,
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error) {
					if capture != nil {
						capture.Store("last", call)
					}
					_ = ctx
					_ = cand
					return lipapi.FixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: text},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}
}
