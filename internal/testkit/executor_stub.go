package testkit

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func stubToolPrefixEvents(call lipapi.Call) []lipapi.Event {
	if len(call.Tools) == 0 {
		return nil
	}
	name := strings.TrimSpace(call.Tools[0].Name)
	if name == "" {
		name = "stub_tool"
	}
	return []lipapi.Event{
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_stub1", ToolName: name},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_stub1", Delta: `{"q":"ok"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_stub1"},
	}
}

func NewStubExecutorWithDeltas(t *testing.T, caps lipapi.BackendCaps, deltas []string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
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
					evs := []lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
					}
					evs = append(evs, stubToolPrefixEvents(call)...)
					for _, d := range deltas {
						evs = append(evs, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d})
					}
					evs = append(evs, lipapi.Event{Kind: lipapi.EventResponseFinished})
					return lipapi.NewFixedEventStream(evs), nil
				},
			},
		},
	}
}

func NewStubExecutor(t *testing.T, caps lipapi.BackendCaps, text string, capture *sync.Map) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
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
					evs := []lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
					}
					evs = append(evs, stubToolPrefixEvents(call)...)
					evs = append(evs,
						lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text},
						lipapi.Event{Kind: lipapi.EventResponseFinished},
					)
					return lipapi.NewFixedEventStream(evs), nil
				},
			},
		},
	}
}
