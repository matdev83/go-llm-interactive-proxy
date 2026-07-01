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
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
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
		Tools: []lipapi.ToolDef{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	want := `[{"type":"message","role":"user","content":"hi"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadInputWireShape_assistantChatCompletionsToolCall(t *testing.T) {
	t.Parallel()
	got := payloadInputJSON(t, lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run it")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{{
					Kind:    lipapi.PartJSON,
					Content: []byte(`{"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo pong\"}"}}`),
				}},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_abc",
					Content:    []byte("pong\n"),
				}},
			},
		},
		Tools: []lipapi.ToolDef{{Name: "bash", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	want := `[{"type":"message","role":"user","content":"run it"},{"type":"function_call","call_id":"call_abc","name":"bash","arguments":"{\"command\":\"echo pong\"}"},{"type":"function_call_output","call_id":"call_abc","output":"{\"output\":\"pong\\n\"}"}]`
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
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
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
		Tools: []lipapi.ToolDef{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	want := `[{"type":"message","role":"user","content":"call the tool"},{"type":"function_call_output","call_id":"call_1","output":"{\"output\":\"{\\\"ok\\\":true}\"}"}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadInputWireShape_noToolsFlattensToolProtocolHistory(t *testing.T) {
	t.Parallel()
	// OpenCode can resend historical assistant tool calls and tool results during
	// no-tools continuation/compaction turns. That history is still useful context,
	// but Codex must not receive it as function_call/function_call_output when the
	// request exposes zero tools. The structured shape caused no-tools turns to
	// hang and sometimes made raw tool-call syntax leak back as assistant text.
	got := payloadInputJSON(t, lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("continue")}},
			{
				Role: lipapi.RoleAssistant,
				Parts: []lipapi.Part{{
					Kind:    lipapi.PartJSON,
					Content: []byte(`{"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo pong\"}"}}`),
				}},
			},
			{
				Role: lipapi.RoleTool,
				Parts: []lipapi.Part{{
					Kind:       lipapi.PartToolResult,
					ToolCallID: "call_abc",
					Content:    []byte(`{"matches":100}`),
				}},
			},
		},
	})
	want := `[{"type":"message","role":"user","content":"continue"},{"type":"message","role":"assistant","content":"Prior assistant tool call (tools unavailable in this request). call_id=call_abc. name=bash. arguments={\"command\":\"echo pong\"}"},{"type":"message","role":"user","content":"Prior tool output (tools unavailable in this request). call_id=call_abc.\n{\"matches\":100}"}]`
	assertJSONEqual(t, got, []byte(want))
}

func TestPayloadForCall_noToolsOmitsToolChoice(t *testing.T) {
	t.Parallel()
	// `tool_choice:auto` is correct only alongside a non-empty `tools` array. A
	// no-tools OpenCode turn should be an ordinary text continuation; sending an
	// auto tool choice in that state tells Codex that a tool protocol may still be
	// available and reintroduces the slow/invalid no-tools behavior this connector
	// already hit in live sessions.
	payload, err := backend.PayloadForCall(&lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("continue")},
		}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"tool_choice"`) {
		t.Fatalf("no-tools payload must omit tool_choice: %s", raw)
	}
	if strings.Contains(string(raw), `"parallel_tool_calls"`) {
		t.Fatalf("no-tools payload must omit parallel_tool_calls: %s", raw)
	}
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
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`),
		}},
		Options: lipapi.GenerationOptions{
			ReasoningEffort:   "high",
			ParallelToolCalls: &parallel,
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}
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
		`"model":"gpt-5.3-codex-spark"`,
		`"store":false`,
		`"instructions":"custom codex instructions"`,
		`"reasoning"`,
		`"effort":"high"`,
		`"summary":"auto"`,
		`"include":["reasoning.encrypted_content"]`,
		`"tool_choice":"auto"`,
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

func compatIgnoreGenParamsExt() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		backend.ExtIgnoreUnsupportedGenParams: json.RawMessage(`true`),
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
	if err == nil {
		t.Fatal("expected error for max_output_tokens without compat extension")
	}
	if !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadForCall_compatDropsMaxOutputTokens(t *testing.T) {
	t.Parallel()
	maxTok := 512
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options:    lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
		Extensions: compatIgnoreGenParamsExt(),
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, key := range []string{`"max_output_tokens"`, `"max_tokens"`} {
		if strings.Contains(s, key) {
			t.Fatalf("payload must not emit %s: %s", key, s)
		}
	}
}

func TestPayloadForCall_ignoresAnthropicMandatoryMaxTokensWithCompatExt(t *testing.T) {
	t.Parallel()
	maxTok := 512
	ext := compatIgnoreGenParamsExt()
	ext["anthropic.model"] = json.RawMessage(`"claude-3-5-haiku-20241022"`)
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options:    lipapi.GenerationOptions{MaxOutputTokens: &maxTok},
		Extensions: ext,
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
	if err == nil {
		t.Fatal("expected error for temperature without compat extension")
	}
	if !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadForCall_compatDropsTemperature(t *testing.T) {
	t.Parallel()
	temp := 0.2
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options:    lipapi.GenerationOptions{Temperature: &temp},
		Extensions: compatIgnoreGenParamsExt(),
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"temperature"`) {
		t.Fatalf("payload must not emit temperature: %s", raw)
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
	if err == nil {
		t.Fatal("expected error for top_p without compat extension")
	}
	if !strings.Contains(err.Error(), "top_p") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadForCall_compatDropsTopP(t *testing.T) {
	t.Parallel()
	topP := 0.9
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Options:    lipapi.GenerationOptions{TopP: &topP},
		Extensions: compatIgnoreGenParamsExt(),
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"top_p"`) {
		t.Fatalf("payload must not emit top_p: %s", raw)
	}
}

func TestPayloadForCall_stripsOpenAIProviderPrefix(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "openai/gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want %q (openai/ provider prefix must be stripped)", payload.Model, "gpt-5.4-mini")
	}
}

func TestPayloadForCall_foldsSystemMessagesIntoInstructions(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("Base instruction.")}}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("You are OpenCode. Use tools.")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instructions, "You are OpenCode. Use tools.") {
		t.Fatalf("instructions must fold system message: %q", payload.Instructions)
	}
	if !strings.Contains(payload.Instructions, "Base instruction.") {
		t.Fatalf("instructions must retain explicit instructions: %q", payload.Instructions)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"role":"system"`) {
		t.Fatalf("payload input must not contain a system role item: %s", raw)
	}
}

func TestPayloadForCall_systemMessageReplacesDefaultInstruction(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("You are my custom agent.")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Instructions != "You are my custom agent." {
		t.Fatalf("instructions = %q, want %q (system message should replace default)", payload.Instructions, "You are my custom agent.")
	}
}

func TestPayloadForCall_doesNotDuplicateSystemMessageAlreadyInInstructions(t *testing.T) {
	t.Parallel()
	bridge := "OpenCode compatibility mode:\n- bridge block"
	call := lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("Base.\n\n" + bridge)}}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart(bridge)}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(payload.Instructions, "OpenCode compatibility mode"); got != 1 {
		t.Fatalf("instructions must not duplicate bridge (count=%d): %q", got, payload.Instructions)
	}
}

func TestPayloadForCall_mergesSystemMessageThatIsSubstringOfInstructions(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Instructions: []lipapi.Message{{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("Be concise and helpful.")}}},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleSystem, Parts: []lipapi.Part{lipapi.TextPart("Be concise.")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}},
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.4-mini"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instructions, "Be concise and helpful.") {
		t.Fatalf("instructions must retain explicit block: %q", payload.Instructions)
	}
	if !strings.Contains(payload.Instructions, "Be concise.") {
		t.Fatalf("instructions must merge system message even when substring of existing block: %q", payload.Instructions)
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}
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
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}
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
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload.Instructions, "OpenCode compatibility mode") {
		t.Fatal("backend must not apply client compat mutations")
	}
}

func TestPayloadForCall_nonHermesToolsRemainStrictAndParallelFalse(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	if !strings.Contains(string(raw), `"strict":true`) {
		t.Fatalf("expected strict=true for non-Hermes: %s", raw)
	}
	if payload.ParallelToolCalls == nil || *payload.ParallelToolCalls {
		t.Fatalf("expected parallel_tool_calls=false for non-Hermes: %+v", payload.ParallelToolCalls)
	}
}

func TestPayloadForCall_hermesToolStrictFalseAndParallelTrue(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools: []lipapi.ToolDef{{Name: "bash"}},
		Extensions: map[string]json.RawMessage{
			backend.ExtToolStrict: json.RawMessage(`false`),
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	if !strings.Contains(string(raw), `"strict":false`) {
		t.Fatalf("expected strict=false for Hermes: %s", raw)
	}
	if payload.ParallelToolCalls == nil || !*payload.ParallelToolCalls {
		t.Fatalf("expected parallel_tool_calls=true for Hermes: %+v", payload.ParallelToolCalls)
	}
}

func TestPayloadForCall_hermesRespectsExplicitParallelFalse(t *testing.T) {
	t.Parallel()
	parallel := false
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
		Tools:   []lipapi.ToolDef{{Name: "bash"}},
		Options: lipapi.GenerationOptions{ParallelToolCalls: &parallel},
		Extensions: map[string]json.RawMessage{
			backend.ExtToolStrict: json.RawMessage(`false`),
		},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if payload.ParallelToolCalls == nil || *payload.ParallelToolCalls {
		t.Fatalf("expected explicit parallel_tool_calls=false honored for Hermes: %+v", payload.ParallelToolCalls)
	}
}

func TestPayloadForCall_mixedAssistantTextAndToolCall(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role: lipapi.RoleAssistant,
			Parts: []lipapi.Part{
				lipapi.TextPart("ok"),
				{
					Kind:    lipapi.PartJSON,
					Content: json.RawMessage(`{"id":"call_abc","type":"function","function":{"name":"legacy_fn","arguments":"{}"}}`),
				},
			},
		}},
		Tools: []lipapi.ToolDef{{Name: "legacy_fn", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	if !strings.Contains(string(raw), `"content":"ok"`) {
		t.Fatalf("missing assistant text item: %s", raw)
	}
	if !strings.Contains(string(raw), `"type":"function_call"`) || !strings.Contains(string(raw), `"call_id":"call_abc"`) {
		t.Fatalf("missing function call item: %s", raw)
	}
}

func TestPayloadForCall_normalizesMissingAdditionalPropertiesForStrict(t *testing.T) {
	t.Parallel()
	strict, raw := codexToolStrict(t, `{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`)
	if !strict {
		t.Fatalf("schema missing only additionalProperties:false should be normalized to strict=true: %s", raw)
	}
}

func TestPayloadForCall_strictCompatibleToolSchemaUsesStrictTrue(t *testing.T) {
	t.Parallel()
	strict, raw := codexToolStrict(t, `{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"],"additionalProperties":false}`)
	if !strict {
		t.Fatalf("strict-compatible schema must keep strict=true: %s", raw)
	}
}

func TestPayloadForCall_parameterlessObjectSchemaGetsAdditionalPropertiesAndRequired(t *testing.T) {
	t.Parallel()
	strict, raw := codexToolStrict(t, `{"type":"object"}`)
	if !strict {
		t.Fatalf("parameterless object schema must stay strict=true after normalization: %s", raw)
	}
	var decoded struct {
		Tools []struct {
			Parameters map[string]any `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tools) != 1 {
		t.Fatalf("tools: %v", decoded.Tools)
	}
	params := decoded.Tools[0].Parameters
	if ap, ok := params["additionalProperties"].(bool); !ok || ap {
		t.Fatalf("parameterless object must have additionalProperties:false: %#v", params)
	}
	req, ok := params["required"].([]any)
	if !ok || len(req) != 0 {
		t.Fatalf("parameterless object must have required:[]: %#v", params)
	}
}

func TestPayloadForCall_parameterlessObjectWithAdditionalPropertiesTrueIsStrictFalse(t *testing.T) {
	t.Parallel()
	// A parameterless object that explicitly allows additional properties is not
	// strict-compatible; it must be sent strict:false so the upstream does not
	// reject it.
	strict, _ := codexToolStrict(t, `{"type":"object","additionalProperties":true}`)
	if strict {
		t.Fatal("parameterless object with additionalProperties:true must use strict=false")
	}
}

func TestPayloadForCall_composedLooseToolSchemaUsesStrictFalse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		schema string
	}{
		{
			name:   "anyOf object missing required",
			schema: `{"type":"object","properties":{"mode":{"anyOf":[{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}]}},"required":["mode"],"additionalProperties":false}`,
		},
		{
			name:   "$ref is conservative false",
			schema: `{"type":"object","properties":{"path":{"$ref":"#/$defs/path"}},"required":["path"],"additionalProperties":false,"$defs":{"path":{"type":"string"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			strict, raw := codexToolStrict(t, tc.schema)
			if strict {
				t.Fatalf("composed/ambiguous schema must use strict=false: %s", raw)
			}
		})
	}
}

func TestPayloadForCall_composedStrictToolSchemaUsesStrictTrue(t *testing.T) {
	t.Parallel()
	strict, raw := codexToolStrict(t, `{"type":"object","properties":{"mode":{"oneOf":[{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}]}},"required":["mode"],"additionalProperties":false}`)
	if !strict {
		t.Fatalf("strict-compatible composed schema must keep strict=true: %s", raw)
	}
}

func codexToolStrict(t *testing.T, schema string) (bool, []byte) {
	t.Helper()
	call := lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}},
		Tools: []lipapi.ToolDef{{
			Name:       "apply_patch",
			Parameters: json.RawMessage(schema),
		}},
	}
	payload, err := backend.PayloadForCall(&call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, backend.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	var decoded struct {
		Tools []struct {
			Strict bool `json:"strict"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tools) != 1 {
		t.Fatalf("tools: %v", decoded.Tools)
	}
	return decoded.Tools[0].Strict, raw
}
