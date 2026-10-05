package openailegacy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

func TestParamsForCall_textOnly(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Route: lipapi.RouteIntent{Selector: "openai-legacy:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"},
		Key:     "openai-legacy:gpt-4o-mini",
	}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "gpt-4o-mini" {
		t.Fatalf("model: %s", p.Model)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"model":"gpt-4o-mini"`) {
		t.Fatalf("marshaled params: %s", raw)
	}
}

func TestParamsForCall_includesStreamUsageByDefault(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "usage-default",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"include_usage":true`) {
		t.Fatalf("expected include_usage true by default: %s", raw)
	}
}

func TestParamsForCall_streamUsageCannotBeDisabledByExtension(t *testing.T) {
	t.Parallel()
	streamOpts, _ := json.Marshal(map[string]bool{"include_usage": false})
	call := lipapi.Call{
		ID: "usage-disabled",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
		Extensions: map[string]json.RawMessage{"openailegacy.stream_options": streamOpts},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"include_usage":true`) {
		t.Fatalf("expected include_usage to be forced on: %s", raw)
	}
}

func TestParamsForCall_modelFromExtensions(t *testing.T) {
	t.Parallel()
	rawModel, _ := json.Marshal("gpt-4o-mini")
	call := lipapi.Call{
		ID: "t2",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"openailegacy.model": rawModel},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Model) != "gpt-4o-mini" {
		t.Fatalf("model: %s", p.Model)
	}
}

func TestParamsForCall_forcesStreamUsage(t *testing.T) {
	t.Parallel()
	rawStreamOptions, _ := json.Marshal(map[string]bool{"include_usage": false})
	call := lipapi.Call{
		ID: "usage",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Extensions: map[string]json.RawMessage{"openailegacy.stream_options": rawStreamOptions},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if !p.StreamOptions.IncludeUsage.Value {
		t.Fatal("expected backend to force stream_options.include_usage=true")
	}
}

func TestParamsForCall_multimodalParts(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "t3",
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("describe"),
				{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
				lipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "minimal.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, "image_url") || !strings.Contains(s, `"type":"file"`) {
		t.Fatalf("expected multimodal markers, got: %s", s)
	}
}

func TestParamsForCall_generationOptions(t *testing.T) {
	t.Parallel()
	temp := 0.7
	top := 0.9
	maxTokens := 128
	parallel := false
	call := lipapi.Call{
		ID: "t4",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("x")},
		}},
		Options: lipapi.GenerationOptions{
			Temperature:       &temp,
			TopP:              &top,
			MaxOutputTokens:   &maxTokens,
			ParallelToolCalls: &parallel,
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"temperature":0.7`) || !strings.Contains(s, `"top_p":0.9`) || !strings.Contains(s, `"max_tokens":128`) {
		t.Fatalf("options: %s", s)
	}
	if !strings.Contains(s, `"parallel_tool_calls":false`) {
		t.Fatalf("expected parallel_tool_calls false: %s", s)
	}
}

func TestParamsForCall_verbosityIsTopLevel(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
		Options:  lipapi.GenerationOptions{Verbosity: lipapi.VerbosityHigh},
	}
	p, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"verbosity":"high"`) {
		t.Fatalf("expected top-level verbosity: %s", raw)
	}
}

func TestParamsForCall_toolsAndToolChoiceWireJSON(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"loc":{"type":"string"}}}`)
	call := lipapi.Call{
		ID: "tools",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("weather?")},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Weather tool",
			Parameters:  schema,
		}},
		ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"tools"`) || !strings.Contains(s, `"function"`) || !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("expected chat tools in wire JSON: %s", s)
	}
	if !strings.Contains(s, `"tool_choice"`) || !strings.Contains(s, `"get_weather"`) {
		t.Fatalf("expected tool_choice with named function: %s", s)
	}
}

func TestParamsForCall_toolResultMessage(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID: "tool-msg",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run tool")}},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_abc",
					Content:    []byte(`{"temp":72}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"role":"tool"`) || !strings.Contains(s, "call_abc") {
		t.Fatalf("expected tool role message with call id: %s", s)
	}
}

func TestUpstreamError_returnsAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error","param":"","code":"invalid_request_error"}}`))
	}))
	t.Cleanup(srv.Close)

	cli := openai.NewClient(
		option.WithBaseURL(srv.URL+"/v1"),
		option.WithAPIKey("sk-test"),
	)
	_, err := cli.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("x"),
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *openai.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", apiErr.StatusCode)
	}
}

// TestParamsForCall_everyCanonicalRoleMapsToItsWireRole pins the complete
// canonical-role to Chat Completions wire-role translation, one canonical message
// per role, because the role vocabulary is this adapter's own provider contract
// rather than a canonical one.
//
// lipapi.RoleDeveloper is included because the generic continuation steering
// transaction writes its instruction as a developer-role message
// (internal/core/runtime/terminal_decision_continuation.go), which the canonical
// projection deliberately does not fold into instructions. Chat Completions has a
// native "developer" wire role, so the canonical role must reach the wire as
// "developer" rather than being rejected or silently re-labelled.
func TestParamsForCall_everyCanonicalRoleMapsToItsWireRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		role     lipapi.Role
		parts    []lipapi.Part
		wantRole string
	}{
		{
			name:     "user",
			role:     lipapi.RoleUser,
			parts:    []lipapi.Part{lipapi.TextPart("U")},
			wantRole: "user",
		},
		{
			name:     "system",
			role:     lipapi.RoleSystem,
			parts:    []lipapi.Part{lipapi.TextPart("S")},
			wantRole: "system",
		},
		{
			name:     "developer",
			role:     lipapi.RoleDeveloper,
			parts:    []lipapi.Part{lipapi.TextPart("D")},
			wantRole: "developer",
		},
		{
			name:     "assistant",
			role:     lipapi.RoleAssistant,
			parts:    []lipapi.Part{lipapi.TextPart("A")},
			wantRole: "assistant",
		},
		{
			name: "tool",
			role: lipapi.RoleTool,
			parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: "call_role_map",
				Content:    []byte(`{"ok":true}`),
			}},
			wantRole: "tool",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := lipapi.Call{
				ID:       "role-map-" + tc.name,
				Messages: []lipapi.Message{{Role: tc.role, Parts: tc.parts}},
			}
			cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
			p, err := backend.ParamsForCall(&call, cand)
			if err != nil {
				t.Fatalf("canonical role %q must be encodable, got error: %v", tc.role, err)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Messages []struct {
					Role string `json:"role"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode wire params %s: %v", raw, err)
			}
			if len(decoded.Messages) != 1 {
				t.Fatalf("canonical role %q produced %d wire messages, want 1; wire=%s", tc.role, len(decoded.Messages), raw)
			}
			if got := decoded.Messages[0].Role; got != tc.wantRole {
				t.Fatalf("canonical role %q mapped to wire role %q, want %q; wire=%s", tc.role, got, tc.wantRole, raw)
			}
		})
	}
}
