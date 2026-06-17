package openailegacy_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(refclienttest.ModuleRoot(t), "testdata", "openailegacy_frontend", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeChat_textNonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "gpt-4o-mini" {
		t.Fatalf("model %q", d.Model)
	}
	if openailegacy.ModelFromCall(d.Call) != "gpt-4o-mini" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:gpt-4o-mini" {
		t.Fatalf("route %q", got)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != lipapi.RoleUser {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if len(d.Call.Messages[0].Parts) != 1 || d.Call.Messages[0].Parts[0].Kind != lipapi.PartText {
		t.Fatalf("parts: %+v", d.Call.Messages[0].Parts)
	}
	if d.Call.Messages[0].Parts[0].Text != "ping" {
		t.Fatalf("text %q", d.Call.Messages[0].Parts[0].Text)
	}
	if d.Call.Tools == nil {
		t.Fatal("expected empty tools slice, got nil")
	}
	if len(d.Call.Tools) != 0 {
		t.Fatalf("tools: %+v", d.Call.Tools)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_textStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeChat_invocationMetadata_nonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.Operation != lipapi.OperationOpenAIChatCompletions {
		t.Fatalf("operation = %q, want %q", d.Call.Invocation.Operation, lipapi.OperationOpenAIChatCompletions)
	}
	if d.Call.Invocation.DeliveryMode != lipapi.DeliveryModeNonStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, lipapi.DeliveryModeNonStreaming)
	}
}

func TestDecodeChat_invocationMetadata_stream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.Operation != lipapi.OperationOpenAIChatCompletions {
		t.Fatalf("operation = %q, want %q", d.Call.Invocation.Operation, lipapi.OperationOpenAIChatCompletions)
	}
	if d.Call.Invocation.DeliveryMode != lipapi.DeliveryModeStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, lipapi.DeliveryModeStreaming)
	}
}

func TestDecodeChat_multimodal(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_multimodal_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := d.Call.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	if parts[0].Kind != lipapi.PartText || parts[0].Text != "describe attachments" {
		t.Fatalf("part0: %+v", parts[0])
	}
	if parts[1].Kind != lipapi.PartImageRef || !strings.Contains(parts[1].ImageRef, "base64,") {
		t.Fatalf("part1: %+v", parts[1])
	}
	if parts[1].ImageMIME != "image/png" {
		t.Fatalf("image mime %q", parts[1].ImageMIME)
	}
	if parts[2].Kind != lipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("part2: %+v", parts[2])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_requiresRoute(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: ""})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeChat_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := openailegacy.DecodeChatRequest([]byte("{"), openailegacy.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeChat_toolsAndToolChoice(t *testing.T) {
	t.Parallel()
	t.Run("auto", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": "auto",
  "tools": [{"type":"function","function":{"name":"fn_a","description":"d","parameters":{"type":"object"}}}]
}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAuto || len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "fn_a" {
			t.Fatalf("got tools=%+v choice=%+v", d.Call.Tools, d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("none_without_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}],"tool_choice":"none"}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != lipapi.ToolChoiceNone {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("function_by_name", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": {"type":"function","function":{"name":"pick"}},
  "tools": [{"type":"function","function":{"name":"pick","parameters":{}}}]
}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != lipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "pick" {
			t.Fatalf("choice %+v", d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("parallel_tool_calls", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "parallel_tool_calls": false,
  "tools": [{"type":"function","function":{"name":"t","parameters":{}}}]
}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.Options.ParallelToolCalls == nil || *d.Call.Options.ParallelToolCalls {
			t.Fatalf("parallel %+v", d.Call.Options.ParallelToolCalls)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeChat_assistantToolCalls(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "assistant",
    "content": null,
    "tool_calls": [{"id":"call_1","type":"function","function":{"name":"x","arguments":"{}"}}]
  }]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || len(d.Call.Messages[0].Parts) != 1 {
		t.Fatalf("parts: %#v", d.Call.Messages)
	}
	if d.Call.Messages[0].Parts[0].Kind != lipapi.PartJSON {
		t.Fatalf("want PartJSON, got %#v", d.Call.Messages[0].Parts[0])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_assistantFunctionCallLegacy(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "assistant",
    "content": "ok",
    "function_call": {"name": "legacy_fn", "arguments": "{}"}
  }]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages[0].Parts) != 2 {
		t.Fatalf("parts: %d", len(d.Call.Messages[0].Parts))
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_toolRoleMessage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "call the tool"},
    {"role": "tool", "tool_call_id": "call_abc", "content": "{\"temp\":21}"}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 2 {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	tm := d.Call.Messages[1]
	if tm.Role != lipapi.RoleTool || len(tm.Parts) != 1 || tm.Parts[0].Kind != lipapi.PartToolResult {
		t.Fatalf("tool message: %+v", tm)
	}
	if tm.Parts[0].ToolCallID != "call_abc" {
		t.Fatalf("tool_call_id: %+v", tm.Parts[0])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_toolRoleMessageArrayContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "call the tool"},
    {"role": "tool", "tool_call_id": "call_abc", "content": [{"type":"text","text":"ok"}]}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	tm := d.Call.Messages[1]
	if len(tm.Parts) != 1 {
		t.Fatalf("tool message parts: %+v", tm.Parts)
	}
	if tm.Parts[0].Kind != lipapi.PartToolResult {
		t.Fatalf("want tool_result part, got %v", tm.Parts[0].Kind)
	}
	testkit.AssertJSONEqual(t, []byte(`[{"type":"text","text":"ok"}]`), []byte(tm.Parts[0].Content))
}

func TestDecodeChat_toolRoleMessageEmptyContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "call the tool"},
    {"role": "tool", "tool_call_id": "call_abc", "content": ""}
  ]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for empty tool content")
	}
}

func TestDecodeChat_streamOptionsExtension(t *testing.T) {
	t.Parallel()
	want := json.RawMessage(`{"include_usage":true}`)
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "stream_options": ` + string(want) + `
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := d.Call.Extensions["openailegacy.stream_options"]
	if !ok {
		t.Fatal("missing stream_options extension")
	}
	if string(raw) != string(want) {
		t.Fatalf("extension got %s want %s", raw, want)
	}
}

// Chat Completions API: tool_choice may be the string "required" (forces model to call a tool).
func TestDecodeChat_toolChoiceRequiredString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": "required",
  "tools": [{"type":"function","function":{"name":"t","parameters":{}}}]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAny || d.Call.ToolChoice.Name != "" {
		t.Fatalf("tool_choice %+v (OpenAI required -> canonical any-tool)", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_developerRoleMapsToSystem(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"developer","content":"You are helpful."}]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != lipapi.RoleSystem {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_unsupportedToolType(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tools": [{"type":"web_search_preview"}]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeChat_unsupportedContentBlockRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "user",
    "content": [{"type":"audio","audio":"AAAA"}]
  }]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for unsupported content block")
	}
	if !strings.Contains(err.Error(), "unsupported content block type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeChat_openRouterBodyFieldsPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "openai/gpt-4o-mini",
  "messages": [{"role":"user","content":"hi"}],
  "provider": {"order":["OpenAI"]},
  "models": ["openai/gpt-4o","anthropic/claude-3.5-sonnet"],
  "route": "fallback",
  "plugins": [{"id":"web"}],
  "debug": true,
  "service_tier": "default",
  "user": "user-123",
  "response_format": {"type":"json_object"}
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "openrouter:openai/gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	ext := d.Call.Extensions
	if openrouterwire.GetRaw(ext, openrouterwire.ExtProvider) == nil {
		t.Error("missing provider extension")
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtModels) == nil {
		t.Error("missing models extension")
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtRoute) != "fallback" {
		t.Errorf("route: %s", ext[openrouterwire.ExtRoute])
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtPlugins) == nil {
		t.Error("missing plugins extension")
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtDebug) == nil {
		t.Error("missing debug extension")
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtServiceTier) != "default" {
		t.Errorf("service_tier: %s", ext[openrouterwire.ExtServiceTier])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtUser) != "user-123" {
		t.Errorf("user: %s", ext[openrouterwire.ExtUser])
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtResponseFormat) == nil {
		t.Error("missing response_format extension")
	}
	if got := openrouterwire.GetString(ext, openrouterwire.ExtUpstreamFlavor); got != openrouterwire.FlavorChat {
		t.Errorf("upstream flavor: got %q want %q", got, openrouterwire.FlavorChat)
	}
}

func TestDecodeChat_openRouterHeadersPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	h := http.Header{}
	h.Set("HTTP-Referer", "https://myapp.com")
	h.Set("X-OpenRouter-Title", "MyApp")
	h.Set("X-OpenRouter-Categories", "ai")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "openrouter:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	ext := d.Call.Extensions
	if openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer) != "https://myapp.com" {
		t.Errorf("referer: %s", ext[openrouterwire.ExtHTTPReferer])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "MyApp" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtCategories) != "ai" {
		t.Errorf("categories: %s", ext[openrouterwire.ExtCategories])
	}
}

func TestDecodeChat_extraBodyFieldsCaptured(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"nvidia/test","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":true},"custom_number":42}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "nvidia:nvidia/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ext := d.Call.Extensions
	ctk := ext[openrouterwire.ExtraBodyExtPrefix+"chat_template_kwargs"]
	if string(ctk) != `{"enable_thinking":true}` {
		t.Errorf("chat_template_kwargs: got %s", ctk)
	}
	cn := ext[openrouterwire.ExtraBodyExtPrefix+"custom_number"]
	if string(cn) != `42` {
		t.Errorf("custom_number: got %s", cn)
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"model"]; ok {
		t.Error("known field 'model' should not be captured as extra_body")
	}
}
