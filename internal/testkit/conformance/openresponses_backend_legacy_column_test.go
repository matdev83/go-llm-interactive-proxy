package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openresponsescompat"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

// openResponsesLegacyFamilyFixture is a canonical legacy message-authority call
// produced by a bundled frontend family. Fixtures identify the source family for
// evidence only: the OpenResponses backend consumes every family through the one
// explicit legacy→ordered-items projector, so no pairwise translator code exists.
type openResponsesLegacyFamilyFixture struct {
	source    string
	call      lipapi.Call
	wantTypes []string
	wantRoles []string
}

func openResponsesLegacyFamilyFixtures() []openResponsesLegacyFamilyFixture {
	return []openResponsesLegacyFamilyFixture{
		{
			source: "openai-chat",
			call: lipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages: []lipapi.Message{
					{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("You are a helpful assistant.")}},
					{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("What is the weather in Paris?")}},
					{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
						Kind:       lipapi.PartJSON,
						ToolCallID: "call_abc",
						ToolName:   "get_weather",
						Content:    json.RawMessage(`{"location":"Paris"}`),
					}}},
					{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
						Kind:       lipapi.PartToolResult,
						ToolCallID: "call_abc",
						ToolName:   "get_weather",
						Text:       "Sunny 22C",
					}}},
				},
				Tools: []lipapi.ToolDef{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
				Extensions: map[string]json.RawMessage{
					"openailegacy.model": json.RawMessage(`"gpt-4o-mini"`),
				},
			},
			wantTypes: []string{"message", "message", "function_call", "function_call_output"},
			wantRoles: []string{"system", "user", "", ""},
		},
		{
			source: "openai-responses",
			call: lipapi.Call{
				Invocation:   openResponsesCreateInvocation(),
				Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("You are concise.")}}},
				Messages: []lipapi.Message{
					{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
					{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{{
						Kind:       lipapi.PartJSON,
						ToolCallID: "call_r1",
						ToolName:   "lookup",
						Content:    json.RawMessage(`{"q":"codex"}`),
					}}},
					{Role: lipapi.RoleTool, Parts: []lipapi.Part{{
						Kind:       lipapi.PartToolResult,
						ToolCallID: "call_r1",
						ToolName:   "lookup",
						Text:       `{"hits":2}`,
					}}},
				},
				Extensions: map[string]json.RawMessage{
					"openairesponses.model": json.RawMessage(`"gpt-5"`),
				},
			},
			wantTypes: []string{"message", "message", "function_call", "function_call_output"},
			wantRoles: []string{"system", "user", "", ""},
		},
		{
			source: "anthropic",
			call: lipapi.Call{
				Invocation:   openResponsesCreateInvocation(),
				Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("Be brief.")}}},
				Messages: []lipapi.Message{
					{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("ping")}},
					{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("pong")}},
					{Role: lipapi.RoleUser, Parts: []lipapi.Part{{
						Kind:      lipapi.PartImageRef,
						ImageRef:  "data:image/png;base64,AAAA",
						ImageMIME: "image/png",
					}}},
				},
				Options: lipapi.GenerationOptions{MaxOutputTokens: new(64)},
				Extensions: map[string]json.RawMessage{
					"anthropic.model": json.RawMessage(`"claude-3-5-haiku-20241022"`),
				},
			},
			wantTypes: []string{"message", "message", "message", "message"},
			wantRoles: []string{"system", "user", "assistant", "user"},
		},
		{
			source: "gemini",
			call: lipapi.Call{
				Invocation:   openResponsesCreateInvocation(),
				Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("answer tersely")}}},
				Messages: []lipapi.Message{
					{Role: lipapi.RoleUser, Parts: []lipapi.Part{
						lipapi.TextPart("describe this"),
						{Kind: lipapi.PartImageRef, ImageRef: "data:image/jpeg;base64,BBBB", ImageMIME: "image/jpeg"},
					}},
					{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("a green field")}},
				},
				Extensions: map[string]json.RawMessage{
					"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
				},
			},
			wantTypes: []string{"message", "message", "message"},
			wantRoles: []string{"system", "user", "assistant"},
		},
	}
}

// openResponsesBackendObserver is an independent request counter for the
// OpenResponses backend column: it counts requests and captures the raw body.
type openResponsesBackendObserver struct {
	mu  sync.Mutex
	n   int
	raw []byte
}

func (o *openResponsesBackendObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.n
}

func (o *openResponsesBackendObserver) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		o.mu.Lock()
		o.n++
		o.raw = append([]byte(nil), body...)
		o.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_native","object":"response","status":"completed","output":[]}`)
	}
}

func newOpenResponsesBackendObserver(t *testing.T, extraCfg string) (*openResponsesBackendObserver, func(lipapi.Call) ([]lipapi.Event, error)) {
	t.Helper()
	obs := &openResponsesBackendObserver{}
	srv := httptest.NewServer(obs.handler())
	t.Cleanup(srv.Close)

	raw := "backend_prefix: my-or\nbase_url: " + srv.URL + "\n" + extraCfg
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	be, err := openresponsescompat.Build("or-inst", n, srv.Client())
	if err != nil {
		t.Fatal(err)
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
	return obs, open
}

//go:fix inline
func intPtr(i int) *int { return new(i) }

func openResponsesCreateInvocation() lipapi.Invocation {
	return lipapi.Invocation{
		Operation:     lipapi.OperationOpenResponsesCreate,
		DeliveryMode:  lipapi.DeliveryModeNonStreaming,
		TransportMode: lipapi.TransportModeNonStreaming,
	}
}

func TestConformance_OpenResponsesBackendColumnLegacyToItems(t *testing.T) {
	t.Parallel()
	for _, fixture := range openResponsesLegacyFamilyFixtures() {
		t.Run(fixture.source, func(t *testing.T) {
			t.Parallel()
			obs, open := newOpenResponsesBackendObserver(t, "")
			if _, err := open(fixture.call); err != nil {
				t.Fatal(err)
			}
			if obs.count() != 1 {
				t.Fatalf("request count = %d, want exactly 1", obs.count())
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(obs.raw, &payload); err != nil {
				t.Fatalf("captured request is not valid JSON: %v", err)
			}
			var input []map[string]json.RawMessage
			if err := json.Unmarshal(payload["input"], &input); err != nil {
				t.Fatalf("input unmarshal: %v", err)
			}
			if len(input) != len(fixture.wantTypes) {
				t.Fatalf("ordered items = %d, want %d", len(input), len(fixture.wantTypes))
			}
			for i, wantType := range fixture.wantTypes {
				if got := string(input[i]["type"]); got != `"`+wantType+`"` {
					t.Fatalf("input[%d].type = %s, want %q", i, got, wantType)
				}
				wantRole := ""
				if i < len(fixture.wantRoles) {
					wantRole = fixture.wantRoles[i]
				}
				if wantRole == "" {
					if _, present := input[i]["role"]; present {
						t.Fatalf("input[%d] must not carry a role, got %q", i, string(input[i]["role"]))
					}
					continue
				}
				if got := string(input[i]["role"]); got != `"`+wantRole+`"` {
					t.Fatalf("input[%d].role = %s, want %q", i, got, wantRole)
				}
			}
			for _, forbidden := range []string{"previous_response_id", `"store"`, `"stream"`, `"background"`} {
				if strings.Contains(string(obs.raw), forbidden) {
					t.Fatalf("captured request forwarded forbidden field %q", forbidden)
				}
			}
		})
	}
}

func TestConformance_OpenResponsesBackendColumnLegacyNoNetwork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		call lipapi.Call
	}{
		{
			name: "conflicting_authority",
			call: lipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Items: []lipapi.Item{{
					Kind: lipapi.ItemKindMessage, ID: "msg_1", Status: lipapi.ItemStatusCompleted,
					Role:    lipapi.RoleUser,
					Content: []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: "x"}},
				}},
				Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("y")}}},
			},
		},
		{
			name: "provider_replay_dialect",
			call: lipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages: []lipapi.Message{{
					Role: lipapi.RoleAssistant,
					Parts: []lipapi.Part{{
						Kind:      lipapi.PartReasoning,
						Reasoning: &lipapi.ReasoningPart{Dialect: lipapi.ReasoningDialectAnthropicThinkingV1, Text: "think"},
					}},
				}},
			},
		},
		{
			name: "source_specific_extension",
			call: lipapi.Call{
				Invocation:   openResponsesCreateInvocation(),
				Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("sys")}}},
				Messages:     []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
				Extensions: map[string]json.RawMessage{
					"anthropic.cache_control": json.RawMessage(`{"ephemeral":true}`),
				},
			},
		},
		{
			name: "unsupported_source_content",
			call: lipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages: []lipapi.Message{{
					Role:  lipapi.RoleUser,
					Parts: []lipapi.Part{{Kind: lipapi.PartToolResult, ToolCallID: "toolu_1", Text: "Sunny"}},
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			obs, open := newOpenResponsesBackendObserver(t, "")
			if _, err := open(tc.call); err == nil {
				t.Fatal("expected pre-network rejection")
			}
			if obs.count() != 0 {
				t.Fatalf("rejection caused %d upstream requests, want 0", obs.count())
			}
		})
	}
}
