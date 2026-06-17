package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestExecutor_decisionLog_backendAttemptOpened_includesInvocationMetadata(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Log:   log,
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"stub": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
					Operation: lipapi.OperationOpenAIChatCompletions,
					Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
				}),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
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

	entry := findDecisionLogEntry(t, buf.Bytes(), "backend_attempt_opened")
	assertLogField(t, entry, "operation", string(lipapi.OperationOpenAIChatCompletions))
	assertLogField(t, entry, "client_delivery_mode", string(lipapi.DeliveryModeNonStreaming))
	assertLogField(t, entry, "upstream_transport_mode", string(lipapi.TransportModeNonStreaming))
	assertLogField(t, entry, "backend", "stub")
	assertLogField(t, entry, "model", "gpt-4o-mini")
	assertLogFieldPresent(t, entry, "candidate_key")
	assertLogFieldPresent(t, entry, "open_duration_ms")
	assertLogFieldPresent(t, entry, "trace_id")
	assertLogFieldPresent(t, entry, "a_leg_id")
}

func TestExecutor_decisionLog_backendAttemptOpened_streamingDelivery(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ex := &runtime.Executor{
		Store: st,
		Bus:   hooks.New(hooks.Config{}),
		Log:   log,
		Rand:  routing.NewSeededRng(2),
		Backends: map[string]execbackend.Backend{
			"stub": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
					Operation: lipapi.OperationOpenAIResponses,
					Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
				}),
				Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
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
		Route: lipapi.RouteIntent{Selector: "stub:o3-mini"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Invocation: lipapi.Invocation{
			Operation:    lipapi.OperationOpenAIResponses,
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

	entry := findDecisionLogEntry(t, buf.Bytes(), "backend_attempt_opened")
	assertLogField(t, entry, "operation", string(lipapi.OperationOpenAIResponses))
	assertLogField(t, entry, "client_delivery_mode", string(lipapi.DeliveryModeStreaming))
	assertLogField(t, entry, "upstream_transport_mode", string(lipapi.TransportModeStreaming))
}

func findDecisionLogEntry(t *testing.T, raw []byte, msg string) map[string]any {
	t.Helper()
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["msg"] == msg {
			return m
		}
	}
	t.Fatalf("no structured %q log entry: %s", msg, string(raw))
	return nil
}

func assertLogField(t *testing.T, entry map[string]any, key, want string) {
	t.Helper()
	got, ok := entry[key]
	if !ok {
		t.Fatalf("log missing %q: %#v", key, entry)
	}
	gotStr, ok := got.(string)
	if !ok {
		t.Fatalf("log field %q = %#v, want string %q", key, got, want)
	}
	if gotStr != want {
		t.Fatalf("log field %q = %q, want %q", key, gotStr, want)
	}
}

func assertLogFieldPresent(t *testing.T, entry map[string]any, key string) {
	t.Helper()
	if _, ok := entry[key]; !ok {
		t.Fatalf("log missing %q: %#v", key, entry)
	}
}
