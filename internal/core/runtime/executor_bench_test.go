package runtime_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// benchEvents returns a realistic-sized stream for hot-path Recv benchmarking.
func benchEvents(nDeltas int) []lipapi.Event {
	ev := make([]lipapi.Event, 0, 3+nDeltas+1)
	ev = append(ev,
		lipapi.Event{Kind: lipapi.EventResponseStarted},
		lipapi.Event{Kind: lipapi.EventMessageStarted},
	)
	for i := 0; i < nDeltas; i++ {
		ev = append(ev, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
	}
	ev = append(ev, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return ev
}

func BenchmarkExecutorExecuteAndDrain32Deltas(b *testing.B) {
	st := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	events := benchEvents(32)
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  rand.New(rand.NewSource(42)),
		Backends: map[string]runtime.Backend{
			"stub": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					return lipapi.NewFixedEventStream(events), nil
				},
			},
		},
	}
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("bench")},
		}},
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		for {
			_, err := s.Recv(ctx)
			if err != nil {
				break
			}
		}
		_ = s.Close()
	}
}
