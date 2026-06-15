package openrouterwire_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
)

func TestCaptureBodyFields_passthrough(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"provider":        json.RawMessage(`{"order":["OpenAI"]}`),
		"models":          json.RawMessage(`["openai/gpt-4o","anthropic/claude-3.5-sonnet"]`),
		"route":           json.RawMessage(`"fallback"`),
		"plugins":         json.RawMessage(`[{"id":"web"}]`),
		"prediction":      json.RawMessage(`{"type":"content","content":"hello"}`),
		"debug":           json.RawMessage(`true`),
		"service_tier":    json.RawMessage(`"default"`),
		"session_id":      json.RawMessage(`"sess-abc"`),
		"user":            json.RawMessage(`"user-123"`),
		"response_format": json.RawMessage(`{"type":"json_object"}`),
		"reasoning":       json.RawMessage(`{"effort":"high"}`),
		"model":           json.RawMessage(`"openai/gpt-4o"`),
		"messages":        json.RawMessage(`[]`),
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureBodyFields(body, ext)

	want := map[string]string{
		openrouterwire.ExtProvider:       `{"order":["OpenAI"]}`,
		openrouterwire.ExtModels:         `["openai/gpt-4o","anthropic/claude-3.5-sonnet"]`,
		openrouterwire.ExtRoute:          `"fallback"`,
		openrouterwire.ExtPlugins:        `[{"id":"web"}]`,
		openrouterwire.ExtPrediction:     `{"type":"content","content":"hello"}`,
		openrouterwire.ExtDebug:          `true`,
		openrouterwire.ExtServiceTier:    `"default"`,
		openrouterwire.ExtSessionID:      `"sess-abc"`,
		openrouterwire.ExtUser:           `"user-123"`,
		openrouterwire.ExtResponseFormat: `{"type":"json_object"}`,
		openrouterwire.ExtReasoning:      `{"effort":"high"}`,
	}
	for key, wantVal := range want {
		got, ok := ext[key]
		if !ok {
			t.Errorf("missing ext %q", key)
			continue
		}
		if string(got) != wantVal {
			t.Errorf("ext[%q] = %s, want %s", key, got, wantVal)
		}
	}

	if _, ok := ext["model"]; ok {
		t.Error("should not capture 'model' field (handled by standard decode)")
	}
	if _, ok := ext["messages"]; ok {
		t.Error("should not capture 'messages' field")
	}
}

func TestCaptureBodyFields_ignoresNull(t *testing.T) {
	t.Parallel()
	body := map[string]json.RawMessage{
		"provider": json.RawMessage(`null`),
		"route":    json.RawMessage(``),
	}
	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureBodyFields(body, ext)
	if len(ext) != 0 {
		t.Errorf("expected empty ext, got %d entries", len(ext))
	}
}

func TestCaptureHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("HTTP-Referer", "https://myapp.com")
	h.Set("X-OpenRouter-Title", "MyApp")
	h.Set("X-OpenRouter-Categories", "ai,chat")
	h.Set("X-OpenRouter-Metadata", `{"session":"abc"}`)

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer) != "https://myapp.com" {
		t.Errorf("referer: %s", ext[openrouterwire.ExtHTTPReferer])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "MyApp" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtCategories) != "ai,chat" {
		t.Errorf("categories: %s", ext[openrouterwire.ExtCategories])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtMetadataHeader) != `{"session":"abc"}` {
		t.Errorf("metadata: %s", ext[openrouterwire.ExtMetadataHeader])
	}
}

func TestCaptureHeaders_xTitleFallback(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-Title", "FallbackTitle")

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "FallbackTitle" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
}

func TestCaptureHeaders_xTitlePrefersFull(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("X-OpenRouter-Title", "Preferred")
	h.Set("X-Title", "Fallback")

	ext := make(map[string]json.RawMessage)
	openrouterwire.CaptureHeaders(h, ext)

	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "Preferred" {
		t.Errorf("title: %s (expected Preferred over Fallback)", ext[openrouterwire.ExtTitle])
	}
}

func TestGetString_empty(t *testing.T) {
	t.Parallel()
	ext := map[string]json.RawMessage{}
	if got := openrouterwire.GetString(ext, "missing"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestGetRaw_nil(t *testing.T) {
	t.Parallel()
	ext := map[string]json.RawMessage{}
	if got := openrouterwire.GetRaw(ext, "missing"); got != nil {
		t.Errorf("expected nil, got %s", got)
	}
}
