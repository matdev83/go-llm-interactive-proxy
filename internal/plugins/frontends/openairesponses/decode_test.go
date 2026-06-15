package openairesponses_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(refclienttest.ModuleRoot(t), "testdata", "openairesponses_frontend", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeCreate_textNonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
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
	if openairesponses.ModelFromCall(d.Call) != "gpt-4o-mini" {
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
	if d.Call.Instructions == nil {
		t.Fatal("expected empty instructions slice, got nil")
	}
	if len(d.Call.Instructions) != 0 {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_textStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeCreate_multimodal(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_multimodal_nonstream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
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

func TestDecodeCreate_requiresRoute(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: ""})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCreate_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := openairesponses.DecodeCreateRequest([]byte("{"), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCreate_functionCallInputItem(t *testing.T) {
	t.Parallel()
	const body = `{"model":"gpt-4o-mini","input":[{"type":"function_call","id":"x","call_id":"c","name":"n","arguments":"{}"}]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 {
		t.Fatalf("messages: %d", len(d.Call.Messages))
	}
	m := d.Call.Messages[0]
	if m.Role != lipapi.RoleAssistant || len(m.Parts) != 1 || m.Parts[0].Kind != lipapi.PartJSON {
		t.Fatalf("unexpected message: %+v", m)
	}
	var functionCall struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(m.Parts[0].Content, &functionCall); err != nil {
		t.Fatalf("part json: %v", err)
	}
	if functionCall.Type != "function_call" || functionCall.ID != "x" || functionCall.CallID != "c" ||
		functionCall.Name != "n" || functionCall.Arguments != "{}" {
		t.Fatalf("function_call payload: %+v", functionCall)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_unsupportedInputItemType(t *testing.T) {
	t.Parallel()
	const body = `{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"r1"}]}`
	_, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCreate_inputString(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":"plain user string"}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != lipapi.RoleUser {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if len(d.Call.Messages[0].Parts) != 1 || d.Call.Messages[0].Parts[0].Text != "plain user string" {
		t.Fatalf("parts: %+v", d.Call.Messages[0].Parts)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_instructionsNonStringRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":"hi","instructions":[{"type":"text","text":"sys"}]}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "instructions must be a JSON string") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_emptyInstructionsYieldsEmptySlice(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":"hi","instructions":"   "}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Instructions == nil {
		t.Fatal("expected empty instructions slice, got nil")
	}
	if len(d.Call.Instructions) != 0 {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
}

func TestDecodeCreate_toolsSamplingAndParallelToolCalls(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "temperature": 0.25,
  "top_p": 0.9,
  "max_output_tokens": 512,
  "parallel_tool_calls": true,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Weather lookup",
        "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
      }
    }
  ],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("tool choice mode %q", d.Call.ToolChoice.Mode)
	}
	if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "get_weather" {
		t.Fatalf("tools: %+v", d.Call.Tools)
	}
	if d.Call.Tools[0].Description != "Weather lookup" {
		t.Fatal(d.Call.Tools[0].Description)
	}
	if string(d.Call.Tools[0].Parameters) == "" {
		t.Fatal("expected parameters JSON")
	}
	if d.Call.Options.Temperature == nil || *d.Call.Options.Temperature != 0.25 {
		t.Fatalf("temperature %+v", d.Call.Options.Temperature)
	}
	if d.Call.Options.TopP == nil || *d.Call.Options.TopP != 0.9 {
		t.Fatalf("top_p %+v", d.Call.Options.TopP)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 512 {
		t.Fatalf("max_output_tokens %+v", d.Call.Options.MaxOutputTokens)
	}
	if d.Call.Options.ParallelToolCalls == nil || !*d.Call.Options.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls %+v", d.Call.Options.ParallelToolCalls)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Flat tool objects (type + name at top level) appear in some OpenAI SDK unions; decode must accept them.
func TestDecodeCreate_toolsFlatSDKShape(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tools": [
    {
      "type": "function",
      "name": "flat_fn",
      "description": "d",
      "parameters": {"type": "object", "properties": {"x": {"type": "string"}}}
    }
  ],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "flat_fn" || d.Call.Tools[0].Description != "d" {
		t.Fatalf("tools: %+v", d.Call.Tools)
	}
	if string(d.Call.Tools[0].Parameters) == "" {
		t.Fatal("expected parameters JSON")
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_toolChoiceFunctionObject(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tool_choice": {"type": "function", "function": {"name": "get_weather"}},
  "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool_choice got %+v", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_toolChoiceRequiredString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tool_choice": "required",
  "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAny || d.Call.ToolChoice.Name != "" {
		t.Fatalf("tool_choice got %+v (OpenAI required -> canonical any-tool)", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_developerRoleMapsToSystem(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type": "message", "role": "developer", "content": "You are helpful."}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
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

func TestDecodeCreate_functionCallOutputItem(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != lipapi.RoleTool {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_unsupportedRoleInMessage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type":"message","role":"narrator","content":"x"}]
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error for unsupported role")
	}
	if !strings.Contains(err.Error(), "unsupported role") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_unsupportedContentBlockInMessage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type":"message","role":"user","content":[{"type":"output_text","text":"x"}]}]
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error for unsupported content block type")
	}
	if !strings.Contains(err.Error(), "unsupported content block type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_unsupportedToolType(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tools": [{"type":"web_search_preview"}],
  "input": [{"type":"message","role":"user","content":"q"}]
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_instructionsPlusMessagesTurn(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "instructions": "You are concise.",
  "input": [
    {"type":"message","role":"user","content":"first"},
    {"type":"message","role":"assistant","content":"ack"},
    {"type":"message","role":"user","content":"second"}
  ]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Parts[0].Text != "You are concise." {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	if len(d.Call.Messages) != 3 {
		t.Fatalf("messages count %d", len(d.Call.Messages))
	}
	if d.Call.Messages[0].Role != lipapi.RoleUser || d.Call.Messages[1].Role != lipapi.RoleAssistant {
		t.Fatalf("roles: %+v / %+v", d.Call.Messages[0].Role, d.Call.Messages[1].Role)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_toolChoice_auto(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tools": [{"type":"function","name":"fn","parameters":{}}],
  "tool_choice": "auto"
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAuto {
		t.Fatalf("mode: %q", d.Call.ToolChoice.Mode)
	}
}

func TestDecodeCreate_toolChoice_none(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tool_choice": "none"
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceNone {
		t.Fatalf("mode: %q", d.Call.ToolChoice.Mode)
	}
}

func TestDecodeCreate_toolChoice_required(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tools": [{"type":"function","name":"fn","parameters":{}}],
  "tool_choice": "required"
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAny {
		t.Fatalf("mode: %q", d.Call.ToolChoice.Mode)
	}
}

func TestDecodeCreate_toolChoice_functionObject(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tools": [{"type":"function","name":"pick_me","parameters":{}}],
  "tool_choice": {"type":"function","function":{"name":"pick_me"}}
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != lipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "pick_me" {
		t.Fatalf("choice: %+v", d.Call.ToolChoice)
	}
}

func TestDecodeCreate_toolChoice_functionObject_missingName(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tool_choice": {"type":"function","function":{}}
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for missing function name")
	}
}

func TestDecodeCreate_modelExtensionMatchesJSONMarshal(t *testing.T) {
	t.Parallel()
	model := "a<b"
	body, err := json.Marshal(map[string]any{
		"model": model,
		"input": "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:x"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	raw := d.Call.Extensions["openairesponses.model"]
	if string(raw) != string(want) {
		t.Fatalf("extension %q want %q", raw, want)
	}
	if openairesponses.ModelFromCall(d.Call) != model {
		t.Fatalf("ModelFromCall got %q", openairesponses.ModelFromCall(d.Call))
	}
}

func TestDecodeCreate_openRouterBodyFieldsPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "openai/gpt-4o-mini",
  "input": "hi",
  "provider": {"order":["OpenAI"]},
  "models": ["openai/gpt-4o"],
  "route": "fallback",
  "plugins": [{"id":"web"}],
  "debug": true,
  "service_tier": "default",
  "session_id": "sess-abc",
  "stop_server_tools_when": "tool_call",
  "reasoning": {"effort":"high"}
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "openrouter:openai/gpt-4o-mini"})
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
	if openrouterwire.GetString(ext, openrouterwire.ExtSessionID) != "sess-abc" {
		t.Errorf("session_id: %s", ext[openrouterwire.ExtSessionID])
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtReasoning) == nil {
		t.Error("missing reasoning extension")
	}
	if got := openrouterwire.GetString(ext, openrouterwire.ExtUpstreamFlavor); got != openrouterwire.FlavorResponses {
		t.Errorf("upstream flavor: got %q want %q", got, openrouterwire.FlavorResponses)
	}
}

func TestDecodeCreate_openRouterHeadersPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi"}`)
	h := http.Header{}
	h.Set("HTTP-Referer", "https://myapp.com")
	h.Set("X-Title", "FallbackTitle")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
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
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "FallbackTitle" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
}
