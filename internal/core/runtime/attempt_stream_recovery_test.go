package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutorStreamRecovery_postOutputEOFEmitsWarningFinishNoRetry(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	ex := &runtime.Executor{
		Store:          st,
		Bus:            hooks.New(hooks.Config{}),
		Rand:           routing.NewSeededRng(1),
		StreamRecovery: streamrecovery.Config{Enabled: true, EmitWarning: true},
		Backends: map[string]execbackend.Backend{
			"openai": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.EventStream, error) {
					opens.Add(1)
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "partial"},
					}), nil
				},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), &lipapi.Call{
		Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var kinds []lipapi.EventKind
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		kinds = append(kinds, ev.Kind)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1", opens.Load())
	}
	if len(kinds) < 2 || kinds[len(kinds)-2] != lipapi.EventWarning || kinds[len(kinds)-1] != lipapi.EventResponseFinished {
		t.Fatalf("tail kinds=%v", kinds)
	}
}
