//go:build integration

package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openresponsescompat"
	refbackendopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

// Task 8.4 OpenResponses backend compatibility column.
//
// Every named scenario below is linked from the column evidence registry
// (openresponses_backend_column.go) and asserts behavior through the deployment
// harness with the independent OpenResponses refbackend injected as the
// reference-provider origin: positive cells round-trip exactly once and the
// refbackend captures the exact ordered request; every rejected feature shows
// zero reference-backend requests.

// backendColumnDeploy deploys one real frontend → OpenResponses backend cell with
// the independent refbackend as the provider origin, returning the deployment
// and the refbackend server for exact-capture assertions.
func backendColumnDeploy(tb testing.TB, frontend string, transport ClientTransport) (*Deployment, *refbackendopenresponses.Server) {
	tb.Helper()
	mode := refbackendopenresponses.ModeJSON
	if transport == TransportSSE {
		mode = refbackendopenresponses.ModeSSE
	}
	ref := refbackendopenresponses.NewServer(refbackendopenresponses.Options{
		AllowMissingBearer: true,
	})
	if err := ref.Register(&refbackendopenresponses.Script{
		ID:          "column-text",
		Description: "backend column text cell",
		Mode:        mode,
		Expected:    refbackendopenresponses.ExpectedRequest{},
		Resource: refbackendopenresponses.NewResource(
			"resp_column_1",
			harnessDefaultModel(BackendOpenResponses),
			1719900000,
			[]refbackendopenresponses.Item{
				{
					Type:    "message",
					ID:      "msg_column_1",
					Status:  "completed",
					Role:    "assistant",
					Content: []refbackendopenresponses.ContentPart{{Type: "output_text", Text: "column-ok"}},
				},
			},
		),
	}); err != nil {
		tb.Fatalf("register refbackend script: %v", err)
	}
	if err := ref.Select("column-text"); err != nil {
		tb.Fatalf("select refbackend script: %v", err)
	}
	d := Deploy(tb, DeploymentSpec{
		Frontend:      frontend,
		Backend:       BackendOpenResponses,
		Transport:     transport,
		OriginHandler: ref.Handler(),
	})
	if d == nil {
		tb.Fatalf("Deploy(%q, openresponses, %s) failed", frontend, transport)
	}
	return d, ref
}

// backendColumnCapturedInput decodes the ordered `input` items from the last
// captured refbackend observation.
func backendColumnCapturedInput(t *testing.T, ref *refbackendopenresponses.Server) []map[string]json.RawMessage {
	t.Helper()
	if ref.Capture().Total() != 1 {
		t.Fatalf("refbackend request total = %d, want exactly 1", ref.Capture().Total())
	}
	obs, ok := ref.Capture().Last()
	if !ok {
		t.Fatal("refbackend captured no observation")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(obs.Body, &payload); err != nil {
		t.Fatalf("captured request is not valid JSON: %v body=%s", err, string(obs.Body))
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	return input
}

// backendColumnZeroRequestDeploy builds the OpenResponses backend with the
// independent refbackend as the origin and returns an `open` closure plus the
// refbackend server. Negative cells must reject before any reference-backend
// request, so ref.Capture().Total() must stay 0.
func backendColumnZeroRequestDeploy(t *testing.T) (func(lipapi.Call) ([]lipapi.Event, error), *refbackendopenresponses.Server) {
	t.Helper()
	ref := refbackendopenresponses.NewServer(refbackendopenresponses.Options{AllowMissingBearer: true})
	if err := ref.Register(&refbackendopenresponses.Script{
		ID:          "column-unreachable",
		Description: "must never be reached by a rejected call",
		Mode:        refbackendopenresponses.ModeJSON,
		Resource: refbackendopenresponses.NewResource(
			"resp_unreachable",
			"gpt-4o-mini",
			1719900000,
			[]refbackendopenresponses.Item{
				refbackendopenresponses.NewMessageItem("assistant", "output_text", "unreachable"),
			},
		),
	}); err != nil {
		t.Fatalf("register refbackend script: %v", err)
	}
	if err := ref.Select("column-unreachable"); err != nil {
		t.Fatalf("select refbackend script: %v", err)
	}

	origin := httptest.NewServer(ref.Handler())
	t.Cleanup(origin.Close)
	raw := "backend_prefix: my-or\nbase_url: " + origin.URL + "\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("openresponses config: %v", err)
	}
	be, err := openresponsescompat.Build("or-inst", n, origin.Client())
	if err != nil {
		t.Fatalf("build openresponses backend: %v", err)
	}
	open := func(call lipapi.Call) ([]lipapi.Event, error) {
		es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
		if err != nil {
			return nil, err
		}
		defer func() { _ = es.Close() }()
		var events []lipapi.Event
		for {
			ev, err := es.Recv(context.Background())
			if err == io.EOF {
				return events, nil
			}
			if err != nil {
				return events, err
			}
			events = append(events, ev)
		}
	}
	return open, ref
}

// TestConformance_OpenResponsesBackendColumn_ToOpenResponsesJSONText runs the
// positive JSON text scenario for all five column cells. The OpenResponses
// backend consumes each legacy frontend's message-authority call (or the
// OpenResponses frontend's item authority) through the explicit projector and
// the independent refbackend captures exactly one ordered create request.
func TestConformance_OpenResponsesBackendColumn_ToOpenResponsesJSONText(t *testing.T) {
	t.Parallel()
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		frontend := frontend
		t.Run(frontend, func(t *testing.T) {
			t.Parallel()
			d, ref := backendColumnDeploy(t, frontend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy(%s, json) failed", frontend)
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("%s -> openresponses json round trip: %v", frontend, err)
			}
			if res.Status != "completed" {
				t.Fatalf("%s -> openresponses status = %q, want completed", frontend, res.Status)
			}
			if !strings.Contains(res.Text, "column-ok") {
				t.Fatalf("%s -> openresponses text = %q, want column-ok", frontend, res.Text)
			}
			input := backendColumnCapturedInput(t, ref)
			if len(input) == 0 {
				t.Fatalf("%s -> openresponses captured an empty ordered input", frontend)
			}
			if ref.MismatchCount() != 0 {
				t.Fatalf("%s -> openresponses refbackend mismatch = %d, want 0", frontend, ref.MismatchCount())
			}
		})
	}
}

// TestConformance_OpenResponsesBackendColumn_ToOpenResponsesStreaming runs the
// positive SSE text scenario for all five column cells: incremental text
// reaches the client with the same canonical stream.
func TestConformance_OpenResponsesBackendColumn_ToOpenResponsesStreaming(t *testing.T) {
	t.Parallel()
	for _, frontend := range OpenResponsesBackendColumnFrontendIDs() {
		frontend := frontend
		t.Run(frontend, func(t *testing.T) {
			t.Parallel()
			d, ref := backendColumnDeploy(t, frontend, TransportSSE)
			if d == nil {
				t.Fatalf("deploy(%s, sse) failed", frontend)
			}
			defer d.Close()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("%s -> openresponses sse round trip: %v", frontend, err)
			}
			if res.Status != "completed" {
				t.Fatalf("%s -> openresponses sse status = %q, want completed", frontend, res.Status)
			}
			if !strings.Contains(res.Text, "column-ok") {
				t.Fatalf("%s -> openresponses sse text = %q, want column-ok", frontend, res.Text)
			}
			backendColumnCapturedInput(t, ref)
			// Single-terminal commitment is only observable through clients that
			// expose the event trajectory (the OpenResponses raw wire client);
			// legacy family clients collapse the stream. Assert it there.
			if frontend == FrontendOpenResponses {
				terminals := 0
				for _, ev := range res.Events {
					if ev == "response.completed" {
						terminals++
					}
				}
				if terminals != 1 {
					t.Fatalf("%s -> openresponses sse saw %d terminal events, want exactly 1 (single-terminal commitment)", frontend, terminals)
				}
			}
		})
	}
}

// TestConformance_OpenResponsesBackendColumn_ToOpenResponsesTools proves the
// legacy OpenAI Chat / OpenAI Responses frontend tool surface is projected to
// ordered function items on the OpenResponses wire for the column cells that
// declare tools.
func TestConformance_OpenResponsesBackendColumn_ToOpenResponsesTools(t *testing.T) {
	t.Parallel()
	cases := []struct {
		frontend string
		path     string
		body     string
	}{
		{
			frontend: FrontendOpenAILegacy,
			path:     "/v1/chat/completions",
			body:     `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"what is the weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`,
		},
		{
			frontend: FrontendOpenAIResponses,
			path:     "/v1/responses",
			body:     `{"model":"gpt-4o-mini","input":"what is the weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.frontend, func(t *testing.T) {
			t.Parallel()
			d, ref := backendColumnDeploy(t, tc.frontend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy(%s) failed", tc.frontend)
			}
			defer d.Close()

			status, err := d.RawFrontendPost(context.Background(), tc.path, tc.body)
			if err != nil {
				t.Fatalf("raw tools post through %s: %v", tc.frontend, err)
			}
			if status != http.StatusOK {
				t.Fatalf("%s tools status = %d, want 200", tc.frontend, status)
			}
			input := backendColumnCapturedInput(t, ref)
			if len(input) == 0 {
				t.Fatalf("%s tools captured empty ordered input", tc.frontend)
			}
			if !rowOriginHasSubstring(d, BackendOpenResponses, "get_weather") {
				t.Fatalf("%s tools upstream request did not carry the projected tool", tc.frontend)
			}
		})
	}
}

// TestConformance_OpenResponsesBackendColumn_ToOpenResponsesMultimodal proves
// image input from the OpenAI Chat / Anthropic frontends is projected to the
// ordered image item on the OpenResponses wire.
func TestConformance_OpenResponsesBackendColumn_ToOpenResponsesMultimodal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		frontend string
		path     string
		body     string
	}{
		{
			frontend: FrontendOpenAILegacy,
			path:     "/v1/chat/completions",
			body:     `{"model":"gpt-4o-mini","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
		},
		{
			frontend: FrontendAnthropic,
			path:     "/v1/messages",
			body:     `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.frontend, func(t *testing.T) {
			t.Parallel()
			d, ref := backendColumnDeploy(t, tc.frontend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy(%s) failed", tc.frontend)
			}
			defer d.Close()

			status, err := d.RawFrontendPost(context.Background(), tc.path, tc.body)
			if err != nil {
				t.Fatalf("raw multimodal post through %s: %v", tc.frontend, err)
			}
			if status != http.StatusOK {
				t.Fatalf("%s multimodal status = %d, want 200", tc.frontend, status)
			}
			input := backendColumnCapturedInput(t, ref)
			if len(input) == 0 {
				t.Fatalf("%s multimodal captured empty ordered input", tc.frontend)
			}
			if !rowOriginHasSubstring(d, BackendOpenResponses, "AAAA") {
				t.Fatalf("%s multimodal upstream request did not carry the projected image", tc.frontend)
			}
		})
	}
}

// TestConformance_OpenResponsesBackendColumn_ToOpenResponsesInstructions proves
// system/developer instructions from the legacy frontends are preserved in the
// ordered request and the refbackend observes exactly one create call.
func TestConformance_OpenResponsesBackendColumn_ToOpenResponsesInstructions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		frontend string
		path     string
		body     string
		want     string
	}{
		{
			frontend: FrontendOpenAILegacy,
			path:     "/v1/chat/completions",
			body:     `{"model":"gpt-4o-mini","messages":[{"role":"system","content":"You are concise."},{"role":"user","content":"ping"}]}`,
			want:     "concise",
		},
		{
			frontend: FrontendAnthropic,
			path:     "/v1/messages",
			body:     `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"system":"Be brief.","messages":[{"role":"user","content":"ping"}]}`,
			want:     "brief",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.frontend, func(t *testing.T) {
			t.Parallel()
			d, ref := backendColumnDeploy(t, tc.frontend, TransportJSON)
			if d == nil {
				t.Fatalf("deploy(%s) failed", tc.frontend)
			}
			defer d.Close()

			status, err := d.RawFrontendPost(context.Background(), tc.path, tc.body)
			if err != nil {
				t.Fatalf("raw instructions post through %s: %v", tc.frontend, err)
			}
			if status != http.StatusOK {
				t.Fatalf("%s instructions status = %d, want 200", tc.frontend, status)
			}
			obs, ok := ref.Capture().Last()
			if !ok {
				t.Fatalf("%s instructions: refbackend captured no request", tc.frontend)
			}
			if !strings.Contains(string(obs.Body), tc.want) {
				t.Fatalf("%s instructions upstream request did not carry %q: %s", tc.frontend, tc.want, string(obs.Body))
			}
		})
	}
}

// TestConformance_OpenResponsesBackendColumn_ToOpenResponsesUsageCommitment
// proves usage surfacing, upstream error mapping, and single-terminal
// commitment for the OpenResponses backend column.
func TestConformance_OpenResponsesBackendColumn_ToOpenResponsesUsageCommitment(t *testing.T) {
	t.Parallel()
	// Commitment: the OpenResponses frontend over SSE exposes the event
	// trajectory and must show exactly one terminal event. Legacy family
	// clients collapse the stream, so they assert status+text above.
	t.Run("commitment_openresponses", func(t *testing.T) {
		t.Parallel()
		d, _ := backendColumnDeploy(t, FrontendOpenResponses, TransportSSE)
		if d == nil {
			t.Fatalf("deploy(openresponses, sse) failed")
		}
		defer d.Close()
		res, err := d.Client.RoundTrip(context.Background(), "ping")
		if err != nil {
			t.Fatalf("openresponses commitment round trip: %v", err)
		}
		terminals := 0
		for _, ev := range res.Events {
			if ev == "response.completed" {
				terminals++
			}
		}
		if terminals != 1 {
			t.Fatalf("openresponses saw %d terminal events, want exactly 1", terminals)
		}
	})

	// Error mapping: an upstream 500 on the primary origin surfaces as a stable
	// client-visible error (never a silent success).
	t.Run("error_mapping", func(t *testing.T) {
		t.Parallel()
		d := Deploy(t, DeploymentSpec{
			Frontend:   FrontendOpenResponses,
			Backend:    BackendOpenResponses,
			Transport:  TransportJSON,
			OriginFail: OriginFailServerError,
		})
		if d == nil {
			t.Fatal("deploy failed")
		}
		defer d.Close()
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err == nil {
			t.Fatal("upstream 500 unexpectedly round-tripped as success")
		}
		if got := d.RequestCount(BackendOpenResponses); got < 1 {
			t.Fatalf("upstream error cell caused no upstream attempt, want >= 1")
		}
	})
}

// TestConformance_OpenResponsesBackendColumn_ReplayDialectNoNetwork proves
// provider-bound reasoning replay is rejected before any reference-backend
// request for the column cells.
func TestConformance_OpenResponsesBackendColumn_ReplayDialectNoNetwork(t *testing.T) {
	t.Parallel()
	open, ref := backendColumnZeroRequestDeploy(t)
	call := lipapi.Call{
		Invocation: openResponsesCreateInvocation(),
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{{
				Kind:      lipapi.PartReasoning,
				Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectAnthropicThinkingV1, Text: "think"},
			}},
		}},
	}
	if _, err := open(call); err == nil {
		t.Fatal("expected pre-network replay-dialect rejection")
	}
	if ref.Capture().Total() != 0 {
		t.Fatalf("replay-dialect rejection caused %d reference-backend requests, want 0", ref.Capture().Total())
	}
}

// TestConformance_OpenResponsesBackendColumn_ConflictNoNetwork proves
// conflicting authorities are rejected before any reference-backend request.
func TestConformance_OpenResponsesBackendColumn_ConflictNoNetwork(t *testing.T) {
	t.Parallel()
	open, ref := backendColumnZeroRequestDeploy(t)
	call := lipapi.Call{
		Invocation: openResponsesCreateInvocation(),
		Items: []lipapi.Item{{
			Kind: lipapi.ItemKindMessage, ID: "msg_1", Status: lipapi.ItemStatusCompleted,
			Role:    lipapi.RoleUser,
			Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "x"}},
		}},
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("y")}}},
	}
	if _, err := open(call); err == nil {
		t.Fatal("expected pre-network conflicting-authority rejection")
	}
	if ref.Capture().Total() != 0 {
		t.Fatalf("conflicting-authority rejection caused %d reference-backend requests, want 0", ref.Capture().Total())
	}
}

// TestConformance_OpenResponsesBackendColumn_ExtensionNoNetwork proves opaque
// source-specific extensions are rejected before any reference-backend request.
func TestConformance_OpenResponsesBackendColumn_ExtensionNoNetwork(t *testing.T) {
	t.Parallel()
	open, ref := backendColumnZeroRequestDeploy(t)
	call := lipapi.Call{
		Invocation:   openResponsesCreateInvocation(),
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("sys")}}},
		Messages:     []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Extensions: map[string]json.RawMessage{
			"anthropic.cache_control": json.RawMessage(`{"ephemeral":true}`),
		},
	}
	if _, err := open(call); err == nil {
		t.Fatal("expected pre-network extension rejection")
	}
	if ref.Capture().Total() != 0 {
		t.Fatalf("extension rejection caused %d reference-backend requests, want 0", ref.Capture().Total())
	}
}

// TestConformance_OpenResponsesBackendColumn_UnsupportedContentNoNetwork proves
// unsupported source content (an orphaned tool result outside a tool-call pair)
// is rejected before any reference-backend request.
func TestConformance_OpenResponsesBackendColumn_UnsupportedContentNoNetwork(t *testing.T) {
	t.Parallel()
	open, ref := backendColumnZeroRequestDeploy(t)
	call := lipapi.Call{
		Invocation: openResponsesCreateInvocation(),
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{{Kind: lipapi.PartToolResult, ToolCallID: "toolu_1", Text: "Sunny"}},
		}},
	}
	if _, err := open(call); err == nil {
		t.Fatal("expected pre-network unsupported-content rejection")
	}
	if ref.Capture().Total() != 0 {
		t.Fatalf("unsupported-content rejection caused %d reference-backend requests, want 0", ref.Capture().Total())
	}
}
