package gemini_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWriteNonStreamJSON_textFromStream(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello-out"},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cands, _ := body["candidates"].([]any)
	if len(cands) < 1 {
		t.Fatalf("candidates: %v", body)
	}
	c0 := cands[0].(map[string]any)
	content := c0["content"].(map[string]any)
	if content["role"] != "model" {
		t.Fatalf("role: %v", content["role"])
	}
	parts := content["parts"].([]any)
	p0 := parts[0].(map[string]any)
	if p0["text"] != "hello-out" {
		t.Fatalf("text: %v", p0["text"])
	}
}

func TestWriteStreamSSE_dataLine(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "Z"},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "data: ") {
		t.Fatalf("body: %q", s)
	}
	if !strings.Contains(s, `"candidates"`) || !strings.Contains(s, `"Z"`) {
		t.Fatalf("body: %q", s)
	}
}

func TestWriteStreamSSE_incrementalTextDeltas(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 0},
		{Kind: lipapi.EventTextDelta, Delta: "hel"},
		{Kind: lipapi.EventTextDelta, Delta: "lo"},
		{Kind: lipapi.EventTextDelta, Delta: " world"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 3},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var texts []string
	var lastIn, lastOut int
	for _, fr := range frames {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		if u, ok := body["usageMetadata"].(map[string]any); ok {
			if x, ok := u["promptTokenCount"].(float64); ok {
				lastIn = int(x)
			}
			if x, ok := u["candidatesTokenCount"].(float64); ok {
				lastOut = int(x)
			}
		}
		cands, _ := body["candidates"].([]any)
		if len(cands) < 1 {
			continue
		}
		c0 := cands[0].(map[string]any)
		content := c0["content"].(map[string]any)
		parts := content["parts"].([]any)
		p0 := parts[0].(map[string]any)
		txt, _ := p0["text"].(string)
		if txt != "" {
			texts = append(texts, txt)
		}
	}
	if len(texts) != 3 || texts[0] != "hel" || texts[1] != "lo" || texts[2] != " world" {
		t.Fatalf("texts %#v", texts)
	}
	if lastIn != 7 || lastOut != 3 {
		t.Fatalf("usageMetadata got in=%d out=%d", lastIn, lastOut)
	}
}

