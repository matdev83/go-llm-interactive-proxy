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
)

func TestExecutor_transportExact_acceptsDeclaredSupport(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := &runtime.Executor{
		Store:                   st,
		Bus:                     hooks.New(hooks.Config{}),
		TransportFallbackPolicy: lipapi.TransportFallbackExact,
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"stub": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
					Operation: lipapi.OperationOpenAIChatCompletions,
					Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
				}),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					if call.Invocation.TransportMode != lipapi.TransportModeStreaming {
						t.Fatalf("transport mode = %q", call.Invocation.TransportMode)
					}
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeStreaming,
		},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("opens = %d", opens)
	}
}

func TestExecutor_transportExact_rejectsMissingSupport(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := &runtime.Executor{
		Store:                   st,
		Bus:                     hooks.New(hooks.Config{}),
		TransportFallbackPolicy: lipapi.TransportFallbackExact,
		Rand:                    routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"stub": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
					Operation: lipapi.OperationOpenAIChatCompletions,
					Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
				}),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					return nil, lipapi.ErrTransportReject
				},
			},
		},
	}

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
	}

	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected transport reject error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("opens = %d", opens)
	}
}

func TestExecutor_transportCompatibility_preservesOmittedCaps(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Rand:  routing.NewSeededRng(1),
		Backends: map[string]execbackend.Backend{
			"legacy": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					atomic.AddInt32(&opens, 1)
					if call.Invocation.TransportMode != lipapi.TransportModeNonStreaming {
						t.Fatalf("transport mode = %q", call.Invocation.TransportMode)
					}
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				},
			},
		},
	}

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "legacy:gpt-4o-mini"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIChatCompletions,
			DeliveryMode: lipapi.DeliveryModeNonStreaming,
		},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("opens = %d", opens)
	}
}
