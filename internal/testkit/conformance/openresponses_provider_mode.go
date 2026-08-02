package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

// ProviderModeModel is the canonical model used by configured provider-mode
// deployments (OpenRouter/NVIDIA OpenAI-compatible provider modes). The value is
// shared with the route-selector the frontend default routes to.
const ProviderModeModel = "gpt-4o-mini"

// openAIResponsesCompatOrigin is a minimal OpenAI Responses-compatible wire
// origin (non-streaming JSON resource and streaming SSE create) used to prove
// the configured OpenAI-compatible provider-mode route. It also serves the
// /models discovery path so the compatible inventory provider can resolve.
type openAIResponsesCompatOrigin struct {
	clock testkitopenresponses.VirtualClock
}

func (h openAIResponsesCompatOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"provider"}]}`)
		return
	}
	var probe struct {
		Stream *bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Stream != nil && *probe.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openAIResponsesCompatSSE(h.created()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, openAIResponsesCompatResource(h.created()))
}

func (h openAIResponsesCompatOrigin) created() int64 {
	if h.clock != nil {
		return h.clock.Now().Unix()
	}
	return 1715620000
}

func openAIResponsesCompatResource(created int64) string {
	payload := map[string]any{
		"id":         "resp_provider_1",
		"object":     "response",
		"created_at": created,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output": []any{
			map[string]any{
				"type":    "message",
				"id":      "msg_provider_1",
				"status":  "completed",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "provider-mode-ok"}},
			},
		},
		"usage": map[string]any{
			"input_tokens":  1,
			"output_tokens": 1,
			"total_tokens":  2,
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// openAIResponsesCompatSSE builds the streaming SSE trajectory of the
// OpenAI-compatible provider-mode origin. Every event payload is constructed as
// a typed value and encoded with encoding/json so no interpolated string can
// corrupt the wire format.
func openAIResponsesCompatSSE(created int64) string {
	txt := "provider-mode-ok"
	item := func(status, text string) map[string]any {
		return map[string]any{
			"type":    "message",
			"id":      "msg_provider_1",
			"status":  status,
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}
	}
	response := func(status string, output []any) map[string]any {
		return map[string]any{
			"id":         "resp_provider_stream",
			"object":     "response",
			"created_at": created,
			"status":     status,
			"model":      "gpt-4o-mini",
			"output":     output,
		}
	}
	events := []struct {
		name string
		data map[string]any
	}{
		{"response.created", map[string]any{
			"type":            "response.created",
			"sequence_number": 1,
			"response":        response("in_progress", []any{}),
		}},
		{"response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": 2,
			"output_index":    0,
			"item":            item("in_progress", ""),
		}},
		{"response.content_part.added", map[string]any{
			"type":            "response.content_part.added",
			"sequence_number": 3,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
			"part":            map[string]any{"type": "output_text", "text": ""},
		}},
		{"response.output_text.delta", map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": 4,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
			"delta":           txt,
		}},
		{"response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": 5,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
			"text":            txt,
		}},
		{"response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": 6,
			"item_id":         "msg_provider_1",
			"output_index":    0,
			"content_index":   0,
		}},
		{"response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": 7,
			"output_index":    0,
			"item":            item("completed", txt),
		}},
		{"response.completed", map[string]any{
			"type":            "response.completed",
			"sequence_number": 8,
			"response":        response("completed", []any{item("completed", txt)}),
		}},
	}
	var buf bytes.Buffer
	for _, ev := range events {
		data, _ := json.Marshal(ev.data)
		buf.WriteString("event: " + ev.name + "\n")
		buf.WriteString("data: " + string(data) + "\n\n")
	}
	buf.WriteString("data: [DONE]\n\n")
	return buf.String()
}

// providerModeTransportCaps declares the OpenAI Responses operation+transport
// surface for the configured provider mode (OpenRouter/NVIDIA).
func providerModeTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIResponses,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

// DeployConfiguredProviderMode deploys the OpenResponses frontend over the
// OpenAI-compatible Responses provider mode (custom-openai-responses-compatible)
// configured with base_url pointing at a fresh origin that emulates an
// OpenAI-compatible provider endpoint (OpenRouter/NVIDIA). The observing origin
// counts and redacts every request so tests can prove the actual configured
// route reaches the provider endpoint. backendID must be one of BackendOpenRouter
// / BackendNVIDIA (evidence identity only; the route is the configured mode).
func DeployConfiguredProviderMode(tb testing.TB, backendID string, transport ClientTransport) *Deployment {
	tb.Helper()
	return DeployConfiguredProviderModeFor(tb, FrontendOpenResponses, backendID, transport)
}

// DeployConfiguredProviderModeFor deploys an arbitrary bundled frontend over the
// configured OpenAI-compatible Responses provider mode backend for the
// OpenRouter/NVIDIA evidence identities. frontend must be a bundled frontend ID;
// only frontends that produce an OpenAI Responses operation (openresponses,
// openai-responses) can round-trip the Responses-wire provider mode; other
// frontend operations fail closed before any upstream request.
func DeployConfiguredProviderModeFor(tb testing.TB, frontend, backendID string, transport ClientTransport) *Deployment {
	tb.Helper()
	if backendID != BackendOpenRouter && backendID != BackendNVIDIA {
		tb.Fatalf("DeployConfiguredProviderMode: unknown provider column %q", backendID)
	}
	d := &Deployment{
		Spec:     DeploymentSpec{Frontend: frontend, Backend: backendID, Transport: transport},
		origins:  map[string]*Origin{},
		backends: map[string]execbackend.Backend{},
	}
	primaryOrigin := newHarnessOrigin(tb, backendID, OriginFailNone, nil, 100, "", nil, openAIResponsesCompatOrigin{})
	d.origins[backendID] = primaryOrigin

	raw := "backend_prefix: provider-or\nbase_url: " + primaryOrigin.URL() + "\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		_ = d.Close()
		tb.Fatalf("harness: provider-mode config: %v", err)
	}
	be, err := openaicompat.BuildCompatible(backendID, "custom-openai-responses-compatible", n, primaryOrigin.Client(), openaicompat.FlavorResponses, providerModeTransportCaps())
	if err != nil {
		_ = d.Close()
		tb.Fatalf("harness: provider-mode backend: %v", err)
	}
	d.backends[backendID] = be

	d.RouteSelector = backendID + ":" + ProviderModeModel
	d.Exec = harnessExecutor(tb, d.backends, backendID)
	d.Mux = http.NewServeMux()
	genCtx, genCancel := context.WithCancel(context.Background())
	d.genCancel = genCancel
	if err := mountHarnessFrontend(d.Mux, frontend, d.Exec, d.RouteSelector, genCtx, 0); err != nil {
		_ = d.Close()
		tb.Fatalf("harness: mount %q frontend: %v", frontend, err)
	}
	d.Server = httptest.NewServer(d.Mux)
	d.Client = harnessClientFor(tb, frontend, d)
	tb.Cleanup(func() { _ = d.Close() })
	return d
}

// ProviderModeRouteSelector returns the route selector the configured
// provider-mode deployment resolves to (backendID:ProviderModeModel).
func ProviderModeRouteSelector(backendID string) string {
	return backendID + ":" + ProviderModeModel
}
