package openaicodex_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got unmarshal: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want unmarshal: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("json mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func payloadInputJSON(t *testing.T, call lipapi.Call) json.RawMessage {
	t.Helper()
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Input
}

func TestPayloadInputWireShape_simpleTextMessage(t *testing.T) {
	t.Parallel()
	got := payloadInputJSON(t, lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	})
	want := `[{"type":"message","role":"user","content":"hello"}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadInputWireShape_multimodalUserMessage(t *testing.T) {
	t.Parallel()
	got := payloadInputJSON(t, lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("describe"),
				{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
				lipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "doc.pdf"),
			},
		}},
	})
	want := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,AAA"},{"type":"input_file","file_data":"QUFB","filename":"doc.pdf"}]}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadInputWireShape_assistantFunctionCallHistory(t *testing.T) {
	t.Parallel()
	got := payloadInputJSON(t, lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{{
					Kind:    lipapi.PartJSON,
					Content: []byte(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`),
				}},
			},
		},
	})
	want := `[{"type":"message","role":"user","content":"hi"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadInputWireShape_rejectsNonFunctionAssistantJSON(t *testing.T) {
	t.Parallel()
	_, err := backend.PayloadForCall(&lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{{
				Kind:    lipapi.PartJSON,
				Content: []byte(`{"type":"other","foo":"bar"}`),
			}},
		}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	}, backend.Config{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported part kind") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadInputWireShape_toolResultMessage(t *testing.T) {
	t.Parallel()
	got := payloadInputJSON(t, lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("call the tool")}},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_1",
					Content:    []byte(`{"ok":true}`),
				}},
			},
		},
	})
	want := `[{"type":"message","role":"user","content":"call the tool"},{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadForCall_modelInstructionsReasoningTemperatureToolsMultimodal(t *testing.T) {
	t.Parallel()
	parallel := true
	call := lipapi.Call{
		ID: "pay1",
		Instructions: []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart("custom codex instructions")},
		}},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser,
			Parts: []lipapi.Part{
				lipapi.TextPart("describe"),
				{Kind: lipapi.PartImageRef, ImageRef: "data:image/png;base64,AAA"},
				lipapi.FilePart("data:application/pdf;base64,QUFB", "application/pdf", "doc.pdf"),
			},
		}},
		Tools: []lipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}},
		Options: lipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &parallel,
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}
	payload, err := backend.PayloadForCall(&call, cand, backend.Config{DefaultReasoningEffort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"model":"gpt-5.3-codex"`,
		`"store":false`,
		`"instructions":"custom codex instructions"`,
		`"reasoning"`,
		`"effort":"high"`,
		`"parallel_tool_calls":true`,
		`input_image`,
		`input_file`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("payload missing %q: %s", want, s)
		}
	}
	if !strings.Contains(s, `"strict":true`) {
		t.Fatalf("payload missing strict: %s", s)
	}
	var decoded struct {
		Tools []struct {
			Type        string         `json:"type"`
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
			Strict      bool           `json:"strict"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tools) != 1 {
		t.Fatalf("tools: %v", decoded.Tools)
	}
	tool := decoded.Tools[0]
	if tool.Type != "function" || tool.Name != "get_weather" || tool.Description != "Get weather" || !tool.Strict {
		t.Fatalf("tool: %+v", tool)
	}
	props, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters: %+v", tool.Parameters)
	}
	city, ok := props["city"].(map[string]any)
	if !ok || city["type"] != "string" {
		t.Fatalf("city property: %+v", props["city"])
	}
	if tool.Parameters["type"] != "object" {
		t.Fatalf("parameters type: %+v", tool.Parameters)
	}
}

func TestPayloadForCall_rejectsMaxOutputTokens(t *testing.T) {
	t.Parallel()
	maxTok := 512
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
	}
	_, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err == nil || !strings.Contains(err.Error(), "max output tokens") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadForCall_ignoresAnthropicMandatoryMaxTokens(t *testing.T) {
	t.Parallel()
	maxTok := 512
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
		Extensions: map[string]json.RawMessage{
			"anthropic.model": json.RawMessage(`"claude-3-5-haiku-20241022"`),
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "max") {
		t.Fatalf("max token cap must not be forwarded to Codex: %s", raw)
	}
}

func TestPayloadForCall_marshalOmitsTemperatureAndTopP(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, key := range []string{`"temperature"`, `"top_p"`} {
		if strings.Contains(s, key) {
			t.Fatalf("payload must not emit %s: %s", key, s)
		}
	}
}

func TestPayloadForCall_rejectsTemperature(t *testing.T) {
	t.Parallel()
	temp := 0.2
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{Temperature: &temp},
	}
	_, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadForCall_rejectsTopP(t *testing.T) {
	t.Parallel()
	topP := 0.9
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options: lipapi.GenerationOptions{TopP: &topP},
	}
	_, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err == nil || !strings.Contains(err.Error(), "top_p") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadForCall_defaultInstructionWhenEmpty(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}
	payload, err := backend.PayloadForCall(&call, cand, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"instructions":`) {
		t.Fatalf("expected default instructions: %s", raw)
	}
}

func TestPayloadForCall_configDefaultsWhenCallUnset(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}
	payload, err := backend.PayloadForCall(&call, cand, backend.Config{
		DefaultReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"effort":"low"`) {
		t.Fatalf("payload: %s", s)
	}
}

func TestPayloadForCall_doesNotMutateForClientMarkers(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Extensions: map[string]json.RawMessage{"agent": json.RawMessage(`"opencode"`)},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload.Instructions, "OpenCode compatibility mode") {
		t.Fatal("backend must not apply client compat mutations")
	}
}
