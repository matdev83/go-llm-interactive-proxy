package gemini_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestDecodeGenerateContent_minimalUserText(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"ping"}]}],
  "generationConfig": {"maxOutputTokens": 128, "temperature": 0.5, "topP": 0.9}
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
		Stream:        false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "gemini-2.0-flash" {
		t.Fatalf("model %q", d.Model)
	}
	if gemini.ModelFromCall(d.Call) != "gemini-2.0-flash" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:gemini-2.0-flash" {
		t.Fatalf("route %q", got)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 128 {
		t.Fatalf("maxOutputTokens: %+v", d.Call.Options.MaxOutputTokens)
	}
	if d.Call.Options.Temperature == nil || *d.Call.Options.Temperature != 0.5 {
		t.Fatalf("temperature: %+v", d.Call.Options.Temperature)
	}
	if d.Call.Options.TopP == nil || *d.Call.Options.TopP != 0.9 {
		t.Fatalf("topP: %+v", d.Call.Options.TopP)
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

func TestDecodeGenerateContent_systemInstruction(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "systemInstruction": {"role":"user","parts":[{"text":"Be brief."}]},
  "contents": [{"role":"user","parts":[{"text":"hi"}]}]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
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

func TestDecodeGenerateContent_streamFlag(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "gemini-2.0-flash",
		Stream:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeGenerateContent_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{`), gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeGenerateContent_missingRouteSelector(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), gemini.DecodeOptions{
		Model: "m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeGenerateContent_missingModel(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), gemini.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeGenerateContent_multimodalInline(t *testing.T) {
	t.Parallel()
	pngB64 := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50})
	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1."))
	body := []byte(`{
  "contents": [{
    "role": "user",
    "parts": [
      {"text": "describe"},
      {"inlineData": {"mimeType": "image/png", "data": "` + pngB64 + `"}},
      {"inline_data": {"mime_type": "application/pdf", "data": "` + pdfB64 + `"}}
    ]
  }]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
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

func TestDecodeGenerateContent_modelRole(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"model","parts":[{"text":"hello"}]}]}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Messages[0].Role != lipapi.RoleAssistant {
		t.Fatalf("role %q", d.Call.Messages[0].Role)
	}
}
