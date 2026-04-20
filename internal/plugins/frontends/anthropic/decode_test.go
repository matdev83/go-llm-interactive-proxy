package anthropic_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDecodeMessage_minimalUserText(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "claude-3-5-haiku-20241022" {
		t.Fatalf("model %q", d.Model)
	}
	if anthropic.ModelFromCall(d.Call) != "claude-3-5-haiku-20241022" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:claude-3-5-haiku-20241022" {
		t.Fatalf("route %q", got)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 64 {
		t.Fatalf("max tokens: %+v", d.Call.Options.MaxOutputTokens)
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

func TestDecodeMessage_systemString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "system": "Be brief.",
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Role != lipapi.RoleSystem {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	if d.Call.Instructions[0].Parts[0].Text != "Be brief." {
		t.Fatal(d.Call.Instructions[0].Parts[0].Text)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_stream(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "stream": true,
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeMessage_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := anthropic.DecodeMessageRequest([]byte(`{`), anthropic.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeMessage_missingRouteSelector(t *testing.T) {
	t.Parallel()
	_, err := anthropic.DecodeMessageRequest([]byte(`{}`), anthropic.DecodeOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeMessage_multimodal(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 128,
  "messages": [{
    "role": "user",
    "content": [
      {"type":"text","text":"describe"},
      {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}},
      {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"BBB"}}
    ]
  }]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := d.Call.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].Kind != lipapi.PartImageRef || !strings.Contains(parts[1].ImageRef, "base64,") {
		t.Fatalf("image part: %+v", parts[1])
	}
	if parts[2].Kind != lipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("file part: %+v", parts[2])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_toolsAndToolChoice(t *testing.T) {
	t.Parallel()
	t.Run("tools_only_auto_choice", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tools": [
    {"name": "get_time", "description": "Clock", "input_schema": {"type": "object", "properties": {}}}
  ]
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "get_time" || d.Call.Tools[0].Description != "Clock" {
			t.Fatalf("tools %+v", d.Call.Tools)
		}
		if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAuto {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tool_choice_none_without_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": {"type": "none"}
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
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
	t.Run("tool_choice_any_with_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": "any",
  "tools": [{"name": "alpha", "input_schema": {"type": "object"}}]
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != lipapi.ToolChoiceAny {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tool_choice_tool_by_name", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": {"type": "tool", "name": "pick_one"},
  "tools": [{"name": "pick_one", "input_schema": {"type": "object"}}]
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != lipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "pick_one" {
			t.Fatalf("choice %+v", d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeMessage_systemBlockArray(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "system": [
    {"type":"text","text":"First block."},
    {"type":"text","text":"Second block."}
  ],
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Role != lipapi.RoleSystem {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	parts := d.Call.Instructions[0].Parts
	if len(parts) != 2 || parts[0].Text != "First block." || parts[1].Text != "Second block." {
		t.Fatalf("system parts: %+v", parts)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_unsupportedRole(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"tool","content":"result"}]
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported message role") {
		t.Fatalf("unexpected err: %v", err)
	}
}
