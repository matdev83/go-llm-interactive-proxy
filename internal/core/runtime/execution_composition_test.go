package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestExecutor_ExecutionCompositionSafety(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	open := func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventMessageStarted},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	}

	backends := map[string]execbackend.Backend{
		"openai":    {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
		"anthropic": {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
		"acp":       {Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming), Open: open},
	}

	classes := map[string]lipsdk.BackendExecutionClass{
		"openai":    lipsdk.BackendExecutionInference,
		"anthropic": lipsdk.BackendExecutionInference,
		"acp":       lipsdk.BackendExecutionAgentRuntime,
	}

	resolver := routing.BackendExecutionResolverFunc(func(id string) (lipsdk.BackendExecutionClass, bool) {
		c, ok := classes[id]
		return c, ok
	})

	newExec := func(policy config.ExecutionCompositionPolicy) *runtime.Executor {
		return runtime.NewExecutor(runtime.ExecutorConfig{
			Core: runtime.CoreRuntime{
				Store:    st,
				Backends: backends,
				Rand:     routing.NewSeededRng(42),
			},
			Extension: runtime.ExtensionRuntime{
				Bus: hooks.New(hooks.Config{}),
			},
			Routing: runtime.RoutingRuntime{
				ExecutionCompositionPolicy: policy,
				BackendExecutionResolver:   resolver,
			},
		})
	}

	t.Run("safe_direct_acp_allowed", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		call := &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "acp:claude-3-7-sonnet"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatalf("expected direct ACP to succeed, got %v", err)
		}
		defer func() { _ = stream.Close() }()
	})

	t.Run("safe_composite_acp_and_inference_rejected", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		tests := []struct {
			name     string
			selector string
		}{
			{name: "failover", selector: "openai:gpt-4o|acp:claude-3-7-sonnet"},
			{name: "first", selector: "[first]acp:claude-3-7-sonnet^[weight=1]openai:gpt-4o"},
			{name: "weighted", selector: "[weight=1]openai:gpt-4o^[weight=1]acp:claude-3-7-sonnet"},
			{name: "parallel", selector: "acp:claude-3-7-sonnet!openai:gpt-4o"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				call := &lipapi.Call{
					Route:    lipapi.RouteIntent{Selector: tc.selector},
					Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
				}
				stream, err := ex.Execute(context.Background(), call)
				if stream != nil {
					_ = stream.Close()
				}
				if err == nil {
					t.Fatalf("expected error for selector %q, got nil", tc.selector)
				}
				if !errors.Is(err, routing.ErrUnsafeExecutionComposition) {
					t.Fatalf("expected ErrUnsafeExecutionComposition, got: %v", err)
				}
				var uErr *routing.UnsafeExecutionCompositionError
				if !errors.As(err, &uErr) {
					t.Fatalf("expected UnsafeExecutionCompositionError, got: %T (%v)", err, err)
				}
			})
		}
	})

	t.Run("safe_composite_pure_inference_allowed", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		call := &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4o|anthropic:claude-3-5-sonnet"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatalf("expected pure inference failover to succeed, got %v", err)
		}
		defer func() { _ = stream.Close() }()
	})

	t.Run("safe_composite_unconfigured_backend_defers_to_missing_backend", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionSafe)
		// "unconfigured" is not in backends map or resolver map
		call := &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4o|unconfigured:model"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if stream != nil {
			_ = stream.Close()
		}
		// A5: Must NOT return ErrUnsafeExecutionComposition
		if errors.Is(err, routing.ErrUnsafeExecutionComposition) {
			t.Fatalf("did not expect ErrUnsafeExecutionComposition for unconfigured backend, got: %v", err)
		}
	})

	t.Run("unrestricted_composite_acp_allowed", func(t *testing.T) {
		ex := newExec(config.ExecutionCompositionUnrestricted)
		call := &lipapi.RouteIntent{Selector: "openai:gpt-4o|acp:claude-3-7-sonnet"}
		_ = call
		callMsg := &lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4o|acp:claude-3-7-sonnet"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}},
		}
		stream, err := ex.Execute(context.Background(), callMsg)
		if err != nil {
			t.Fatalf("expected unrestricted composition to succeed, got %v", err)
		}
		defer func() { _ = stream.Close() }()
	})
}
