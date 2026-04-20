package openailegacy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWriteNonStreamJSON_matchesGolden(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "golden-ok"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{
		CompletionID: "chatcmpl_encode_golden",
		CreatedAt:    1715620000,
	}
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := readGolden(t, "chat_completion_nonstream_expected.json")
	assertJSONEqual(t, want, rec.Body.Bytes())
}

func TestWriteNonStreamJSON_usageFromCollect(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 0},
		{Kind: lipapi.EventTextDelta, Delta: "x"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 1},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_usage_ns", CreatedAt: 1715620000}
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.Prompt != 4 || v.Usage.Completion != 1 || v.Usage.Total != 5 {
		t.Fatalf("usage %+v", v.Usage)
	}
}

func TestWriteErrorJSON_shape(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	openailegacy.WriteErrorJSON(rec, 400, "bad", "invalid_request_error", "empty")
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	errObj, ok := v["error"].(map[string]any)
	if !ok {
		t.Fatalf("body %s", rec.Body.String())
	}
	if errObj["message"] != "bad" {
		t.Fatal(errObj)
	}
}

func TestWriteStreamSSE_roleContentDone(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "stream-ok"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_stream_ut", CreatedAt: 1715620000}
	if err := openailegacy.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "chat.completion.chunk") {
		t.Fatalf("missing chunk object: %q", s)
	}
	if !strings.Contains(s, "stream-ok") {
		t.Fatalf("missing text: %q", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("missing done: %q", s)
	}
}

func TestWriteStreamSSE_incrementalTextDeltas(t *testing.T) {
	t.Parallel()
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
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	opts := openailegacy.EncodeOptions{CompletionID: "chatcmpl_stream_incr", CreatedAt: 1715620000}
	if err := openailegacy.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var contentChunks []string
	var lastUsage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
	}
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
			continue
		}
		var v struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta *struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Object != "chat.completion.chunk" {
			continue
		}
		if len(v.Choices) != 1 || v.Choices[0].Delta == nil {
			continue
		}
		d := v.Choices[0].Delta
		if d.Role != "" {
			continue
		}
		if v.Choices[0].FinishReason != nil {
			if v.Usage != nil {
				lastUsage.Prompt = v.Usage.Prompt
				lastUsage.Completion = v.Usage.Completion
			}
			continue
		}
		if d.Content != "" {
			contentChunks = append(contentChunks, d.Content)
		}
	}
	if len(contentChunks) != 3 || contentChunks[0] != "hel" || contentChunks[1] != "lo" || contentChunks[2] != " world" {
		t.Fatalf("content chunks: %#v", contentChunks)
	}
	if lastUsage.Prompt != 7 || lastUsage.Completion != 3 {
		t.Fatalf("usage got %+v", lastUsage)
	}
}

func mustModelExt(tb testing.TB, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{"openailegacy.model": raw}
}

func assertJSONEqual(t *testing.T, wantJSON, gotJSON []byte) {
	t.Helper()
	var want, got any
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		t.Fatalf("got unmarshal: %v body=%s", err, string(gotJSON))
	}
	if !bytes.Equal(mustNormJSON(t, want), mustNormJSON(t, got)) {
		t.Fatalf("json mismatch\nwant: %s\ngot:  %s", string(wantJSON), string(gotJSON))
	}
}

func mustNormJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
