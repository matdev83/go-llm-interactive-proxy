package openailegacy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
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

func TestDecodeChat_assistantToolCallsRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "assistant",
    "content": null,
    "tool_calls": [{"id":"call_1","type":"function","function":{"name":"x","arguments":"{}"}}]
  }]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tool_calls") {
		t.Fatalf("unexpected err: %v", err)
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
