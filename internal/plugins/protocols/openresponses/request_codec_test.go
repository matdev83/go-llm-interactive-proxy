package openresponses

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRequestCodec_OfficialGolden(t *testing.T) {
	t.Parallel()
	paramPath := filepath.Join("testdata", "official_examples", "ResponseParam.json")
	data, err := os.ReadFile(paramPath)
	if err != nil {
		t.Fatalf("failed to read official ResponseParam.json: %v", err)
	}

	param, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed on official golden: %v", err)
	}

	if !call.HasItemAuthority() {
		t.Fatalf("expected call to have item authority")
	}

	if len(call.Items) != 1 {
		t.Fatalf("expected 1 item in call, got %d", len(call.Items))
	}

	item := call.Items[0]
	if item.Kind != lipapi.ItemKindMessage {
		t.Fatalf("expected item kind message, got %q", item.Kind)
	}
	if item.Role != lipapi.RoleUser {
		t.Fatalf("expected role user, got %q", item.Role)
	}
	if len(item.Content) != 1 || item.Content[0].Kind != lipapi.ContentPartText {
		t.Fatalf("expected 1 text content part, got %+v", item.Content)
	}
	if item.Content[0].Text != "What's the weather like in Paris today?" {
		t.Fatalf("expected text content, got %q", item.Content[0].Text)
	}

	reEncoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}

	_, reDecodedCall, err := DecodeRequest(reEncoded)
	if err != nil {
		t.Fatalf("DecodeRequest failed on re-encoded request: %v", err)
	}

	if len(reDecodedCall.Items) != len(call.Items) {
		t.Fatalf("re-decoded items count mismatch: want %d, got %d", len(call.Items), len(reDecodedCall.Items))
	}

	_ = param
}

func TestRequestCodec_StringInput(t *testing.T) {
	t.Parallel()
	data := []byte(`{"model": "gpt-4o", "input": "Hello world"}`)
	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for string input: %v", err)
	}

	if !call.HasItemAuthority() {
		t.Fatalf("expected item authority for string input")
	}
	if len(call.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(call.Items))
	}
	if call.Items[0].Role != lipapi.RoleUser || call.Items[0].Content[0].Text != "Hello world" {
		t.Fatalf("unexpected item for string input: %+v", call.Items[0])
	}
}

func TestRequestCodec_ItemUnionForms(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": [
			{"type": "message", "id": "msg_1", "role": "system", "content": [{"type": "input_text", "text": "System prompt"}]},
			{"type": "item_reference", "id": "msg_prev"},
			{"type": "function_call", "id": "call_1", "call_id": "call_123", "name": "get_weather", "arguments": "{\"location\":\"Paris\"}"},
			{"type": "function_call_output", "call_id": "call_123", "output": "Sunny 22C"},
			{"type": "reasoning", "id": "reas_1", "reasoning": "Thinking step..."},
			{"type": "compaction", "id": "comp_1", "encapsulated_id": "enc_1", "dialect": "standard", "implementor": "proxy"},
			{"type": "acme:custom_type", "id": "ext_1", "namespace": "custom", "direction": "in", "data": {"key": "val"}}
		]
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for item union forms: %v", err)
	}

	if len(call.Items) != 7 {
		t.Fatalf("expected 7 items, got %d", len(call.Items))
	}

	if call.Items[0].Kind != lipapi.ItemKindMessage || call.Items[0].Role != lipapi.RoleSystem {
		t.Errorf("item 0 mismatch: %+v", call.Items[0])
	}
	if call.Items[1].Kind != lipapi.ItemKindItemReference || call.Items[1].Reference.ID != "msg_prev" {
		t.Errorf("item 1 mismatch: %+v", call.Items[1])
	}
	if call.Items[2].Kind != lipapi.ItemKindToolCall || call.Items[2].ToolCall.Name != "get_weather" {
		t.Errorf("item 2 mismatch: %+v", call.Items[2])
	}
	if call.Items[3].Kind != lipapi.ItemKindToolResult || call.Items[3].ToolResult.Output != "Sunny 22C" {
		t.Errorf("item 3 mismatch: %+v", call.Items[3])
	}
	if call.Items[4].Kind != lipapi.ItemKindReasoning || call.Items[4].Reasoning == nil {
		t.Errorf("item 4 mismatch: %+v", call.Items[4])
	}
	if call.Items[5].Kind != lipapi.ItemKindCompaction || call.Items[5].Compaction.EncapsulatedID != "enc_1" {
		t.Errorf("item 5 mismatch: %+v", call.Items[5])
	}
	if call.Items[6].Kind != lipapi.ItemKindExtension || call.Items[6].Extension.Namespace != "custom" {
		t.Errorf("item 6 mismatch: %+v", call.Items[6])
	}
}

func TestRequestCodec_ContentUnionForms(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Text part"},
					{"type": "input_image", "image_url": "https://example.com/a.png"},
					{"type": "input_image", "image_url": {"url": "https://example.com/b.png"}},
					{"type": "refusal", "refusal": "Cannot fulfill request"}
				]
			}
		]
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for content union forms: %v", err)
	}

	if len(call.Items[0].Content) != 4 {
		t.Fatalf("expected 4 content parts, got %d", len(call.Items[0].Content))
	}
	if call.Items[0].Content[1].ImageRef != "https://example.com/a.png" {
		t.Errorf("image_url string mismatch: got %q", call.Items[0].Content[1].ImageRef)
	}
	if call.Items[0].Content[2].ImageRef != "https://example.com/b.png" {
		t.Errorf("image_url object mismatch: got %q", call.Items[0].Content[2].ImageRef)
	}
}

func TestRequestCodec_ToolAndControlParameters(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": "test",
		"tools": [
			{"type": "function", "name": "fn1", "description": "desc", "parameters": {"type": "object"}}
		],
		"tool_choice": "required",
		"temperature": 0.5,
		"top_p": 0.9,
		"max_output_tokens": 100,
		"store": true
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for tool and control parameters: %v", err)
	}

	if len(call.Tools) != 1 || call.Tools[0].Name != "fn1" {
		t.Fatalf("tool decoding mismatch: %+v", call.Tools)
	}
	if call.ToolChoice.Mode != lipapi.ToolChoiceAny {
		t.Fatalf("tool_choice mode mismatch: got %v", call.ToolChoice.Mode)
	}
	if call.Options.Temperature == nil || *call.Options.Temperature != 0.5 {
		t.Fatalf("temperature mismatch: %v", call.Options.Temperature)
	}
}

func TestRequestCodec_AllowedTools(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": "test",
		"tools": [
			{"type": "function", "name": "fn1", "description": "desc", "parameters": {"type": "object"}}
		],
		"tool_choice": {
			"type": "allowed_tools",
			"tools": [{"type": "function", "name": "fn1"}]
		}
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for allowed_tools tool_choice: %v", err)
	}

	if call.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("expected ToolChoiceAuto mode, got %v", call.ToolChoice.Mode)
	}
}

func TestRequestCodec_PreviousResponseID_Continuation(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"previous_response_id": "resp_123"
	}`)

	param, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for previous_response_id continuation: %v", err)
	}

	if param.PreviousResponseID == nil || *param.PreviousResponseID != "resp_123" {
		t.Fatalf("previous_response_id mismatch: %v", param.PreviousResponseID)
	}
	if len(call.Items) != 0 {
		t.Fatalf("expected 0 items for absent input continuation, got %d", len(call.Items))
	}
}

func TestRequestCodec_NegativeTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "MalformedJSON",
			json:    `{"model": "gpt-4o", "input": `,
			wantErr: "decode failed",
		},
		{
			name:    "DuplicateKey",
			json:    `{"model": "gpt-4o", "model": "gpt-3.5", "input": "test"}`,
			wantErr: "duplicate key",
		},
		{
			name:    "InvalidUTF8",
			json:    string([]byte{'{', '"', 'm', 'o', 'd', 'e', 'l', '"', ':', ' ', 0xff, '}'}),
			wantErr: "invalid UTF-8",
		},
		{
			name:    "UnknownUnprefixedDiscriminator",
			json:    `{"model": "gpt-4o", "input": [{"type": "unknown_magic_type"}]}`,
			wantErr: "unknown unprefixed discriminator",
		},
		{
			name:    "InventedContentDiscriminator",
			json:    `{"model": "gpt-4o", "input": [{"type": "message", "role": "user", "content": [{"type": "input_video", "video_url": "http://example.com/v.mp4"}]}]}`,
			wantErr: "unknown unprefixed discriminator",
		},
		{
			name:    "UnsupportedBackgroundMode",
			json:    `{"model": "gpt-4o", "input": "hi", "background": true}`,
			wantErr: "background execution mode is unsupported",
		},
		{
			name:    "TrailingData",
			json:    `{"model": "gpt-4o", "input": "hi"} {"extra": 1}`,
			wantErr: "trailing data",
		},
		{
			name:    "InvalidInputType",
			json:    `{"model": "gpt-4o", "input": 12345}`,
			wantErr: "decode failed",
		},
		{
			name:    "MissingInputWithoutPreviousID",
			json:    `{"model": "gpt-4o"}`,
			wantErr: "input is required",
		},
		{
			name:    "ConflictingAuthority",
			json:    `{"model": "gpt-4o", "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}], "messages": [{"role": "user", "content": "hi"}]}`,
			wantErr: "conflicting raw item and legacy message authorities",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := DecodeRequest([]byte(tt.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
		})
	}
}

func TestRequestCodec_Immutability(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"model": "gpt-4o", "input": "Hello world"}`)

	_, call, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest failed: %v", err)
	}

	// Mutate source bytes
	for i := range raw {
		raw[i] = 0xFF
	}

	// Ensure decoded call content was not mutated
	if call.Items[0].Content[0].Text != "Hello world" {
		t.Fatalf("canonical call text was mutated when source bytes changed")
	}
}

func TestRequestCodec_WireDepthUsesConfiguredLimit(t *testing.T) {
	depth := 17
	parameters := strings.Repeat(`{"nested":`, depth) + `1` + strings.Repeat(`}`, depth)
	body := []byte(`{"model":"gpt-4o","input":"hi","tools":[{"type":"function","name":"deep","parameters":` + parameters + `}]}`)
	limits := DefaultLimits()
	limits.MaxItemDepth = 1
	if _, _, err := DecodeRequest(body, limits); err == nil || !strings.Contains(err.Error(), "exceeds limit 1") {
		t.Fatalf("wire JSON must use MaxItemDepth, got: %v", err)
	}
}

func TestRequestCodec_EmptyToolResultPreservesOutputField(t *testing.T) {
	wire, err := EncodeItem(lipapi.Item{
		Kind: lipapi.ItemKindToolResult,
		ToolResult: &lipapi.ToolResultItem{
			CallID: "call_empty",
		},
	})
	if err != nil {
		t.Fatalf("EncodeItem failed: %v", err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal encoded item: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode encoded item: %v", err)
	}
	if got := string(fields["output"]); got != `""` {
		t.Fatalf("output field = %s, want empty JSON string", got)
	}
}

func TestRequestCodec_MessageContentStringShorthand(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		role     string
		phase    string
		expected string
	}{
		"user":         {role: "user", expected: "Say hello in exactly 3 words."},
		"assistant":    {role: "assistant", expected: "I should answer with the saved number."},
		"system":       {role: "system", expected: "You are a pirate. Always respond in pirate speak."},
		"commentary":   {role: "assistant", phase: "commentary", expected: "I should answer with the saved number."},
		"final_answer": {role: "assistant", phase: "final_answer", expected: "The number is four."},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := map[string]any{"type": "message", "role": tc.role, "content": tc.expected}
			if tc.phase != "" {
				item["phase"] = tc.phase
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-4o", "input": []any{item}})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("DecodeRequest failed for %s content string shorthand: %v", tc.role, err)
			}
			if len(call.Items) != 1 {
				t.Fatalf("expected 1 item, got %d", len(call.Items))
			}
			it := call.Items[0]
			if it.Kind != lipapi.ItemKindMessage || it.Role != lipapi.Role(tc.role) {
				t.Fatalf("item kind/role mismatch: %+v", it)
			}
			if tc.phase != "" && it.Phase != lipapi.AssistantPhase(tc.phase) {
				t.Fatalf("phase mismatch: got %q want %q", it.Phase, tc.phase)
			}
			if len(it.Content) != 1 || it.Content[0].Kind != lipapi.ContentPartText {
				t.Fatalf("expected 1 text part, got %+v", it.Content)
			}
			if it.Content[0].Text != tc.expected {
				t.Fatalf("text mismatch: got %q want %q", it.Content[0].Text, tc.expected)
			}
		})
	}
}

func TestRequestCodec_DeterministicEncode(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Items: []lipapi.Item{
			{
				Kind:   lipapi.ItemKindMessage,
				Role:   lipapi.RoleUser,
				Status: lipapi.ItemStatusCompleted,
				Content: []lipapi.ContentPart{
					{Kind: lipapi.ContentPartText, Text: "Deterministic test"},
				},
			},
		},
	}

	b1, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest 1 failed: %v", err)
	}

	b2, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest 2 failed: %v", err)
	}

	if !bytes.Equal(b1, b2) {
		t.Fatalf("EncodeRequest is not deterministic: %s != %s", string(b1), string(b2))
	}
}
