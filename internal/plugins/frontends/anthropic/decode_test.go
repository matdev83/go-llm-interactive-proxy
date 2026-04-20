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
