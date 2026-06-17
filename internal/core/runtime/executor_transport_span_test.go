package runtime_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

//nolint:paralleltest // Mutates global OpenTelemetry tracer provider; must run serially.
func TestExecutor_transportRejectSpan_recordsDecisionAttributes(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
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
					t.Fatal("backend must not open after transport reject")
					return nil, nil
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
		t.Fatal("expected transport reject")
	}

	span := findEndedSpan(t, rec.Ended(), "lip.executor.transport_negotiate")
	if span.Status().Code != codes.Error {
		t.Fatalf("span status = %v, want error", span.Status())
	}
	assertSpanAttr(t, span.Attributes(), "lip.operation", string(lipapi.OperationOpenAIChatCompletions))
	assertSpanAttr(t, span.Attributes(), "lip.client_delivery_mode", string(lipapi.DeliveryModeNonStreaming))
	assertSpanAttr(t, span.Attributes(), "lip.transport_mode", string(lipapi.TransportModeNonStreaming))
	assertSpanAttr(t, span.Attributes(), "lip.transport_negotiation_kind", string(lipapi.NegotiationReject))
}

func findEndedSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for i := len(spans) - 1; i >= 0; i-- {
		if spans[i].Name() == name {
			return spans[i]
		}
	}
	t.Fatalf("missing span %q; ended=%d", name, len(spans))
	return nil
}

func assertSpanAttr(t *testing.T, attrs []attribute.KeyValue, key, want string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			if got := attr.Value.AsString(); got != want {
				t.Fatalf("span attr %q = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Fatalf("span missing attr %q: %#v", key, attrs)
}
