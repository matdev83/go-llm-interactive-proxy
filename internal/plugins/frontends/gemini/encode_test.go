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
	es := lipapi.NewFixedEventStream([]lipapi.Event{
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
	c0 := testkit.MustMapStringAny(t, cands[0])
	content := testkit.MustMapStringAny(t, c0["content"])
	if content["role"] != "model" {
		t.Fatalf("role: %v", content["role"])
	}
	parts := testkit.MustSliceAny(t, content["parts"])
	p0 := testkit.MustMapStringAny(t, parts[0])
	if p0["text"] != "hello-out" {
		t.Fatalf("text: %v", p0["text"])
	}
	if _, has := body["usageMetadata"]; has {
		t.Fatalf("non-stream JSON must not include usageMetadata (subset contract); body=%v", body)
	}
}

func TestWriteNonStreamJSON_ignoresUsageFromStream(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 5},
		{Kind: lipapi.EventTextDelta, Delta: "out"},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, has := body["usageMetadata"]; has {
		t.Fatalf("expected no usageMetadata on non-stream response; body=%v", body)
	}
}

func TestWriteStreamSSE_dataLine(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	es := lipapi.NewFixedEventStream([]lipapi.Event{
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
	es := lipapi.NewFixedEventStream([]lipapi.Event{
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
		c0 := testkit.MustMapStringAny(t, cands[0])
		content := testkit.MustMapStringAny(t, c0["content"])
		parts := testkit.MustSliceAny(t, content["parts"])
		p0 := testkit.MustMapStringAny(t, parts[0])
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

func TestWriteStreamSSEUsesClientVisibleScopedUsage(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{
			{InputTokens: 100, OutputTokens: 50, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable}},
			{InputTokens: 10, OutputTokens: 5, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneClientVisible}},
		}},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()

	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"promptTokenCount":10`) || !strings.Contains(body, `"candidatesTokenCount":5`) || !strings.Contains(body, `"totalTokenCount":15`) {
		t.Fatalf("stream body %q does not contain client-visible usage", body)
	}
	if strings.Contains(body, `"promptTokenCount":100`) || strings.Contains(body, `"candidatesTokenCount":50`) {
		t.Fatalf("stream body %q contains provider usage", body)
	}
}

func TestWriteStreamSSE_functionCallChunk(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "pre"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "fc_1", ToolName: "compute"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "fc_1", Delta: `{"n":`},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "fc_1", Delta: `42}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "fc_1"},
		{Kind: lipapi.EventTextDelta, Delta: "post"},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteStreamSSE(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var fcSeen bool
	for _, fr := range frames {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &body); err != nil {
			t.Fatal(err)
		}
		cands, _ := body["candidates"].([]any)
		if len(cands) == 0 {
			continue
		}
		c0 := testkit.MustMapStringAny(t, cands[0])
		content := testkit.MustMapStringAny(t, c0["content"])
		parts := testkit.MustSliceAny(t, content["parts"])
		for _, p := range parts {
			pm := testkit.MustMapStringAny(t, p)
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				fcSeen = true
				if fc["name"] != "compute" {
					t.Fatalf("functionCall name: %v", fc["name"])
				}
				args, _ := fc["args"].(map[string]any)
				if args["n"] != float64(42) {
					t.Fatalf("functionCall args: %v", args)
				}
			}
		}
	}
	if !fcSeen {
		t.Fatal("expected functionCall in stream chunks")
	}
}

func TestWriteNonStreamJSON_functionCallOutput(t *testing.T) {
	t.Parallel()
	call := &lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
	}
	call.Extensions = map[string]json.RawMessage{
		"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
	}
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "fc_2", ToolName: "search"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "fc_2", Delta: `{"q":"test"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "fc_2"},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := gemini.WriteNonStreamJSON(context.Background(), rec, call, es, gemini.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cands, _ := body["candidates"].([]any)
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	c0 := testkit.MustMapStringAny(t, cands[0])
	content := testkit.MustMapStringAny(t, c0["content"])
	parts := testkit.MustSliceAny(t, content["parts"])
	var fcSeen bool
	for _, p := range parts {
		pm := testkit.MustMapStringAny(t, p)
		if fc, ok := pm["functionCall"].(map[string]any); ok {
			fcSeen = true
			if fc["name"] != "search" {
				t.Fatalf("name: %v", fc["name"])
			}
		}
	}
	if !fcSeen {
		t.Fatalf("missing functionCall: %+v", parts)
	}
}
