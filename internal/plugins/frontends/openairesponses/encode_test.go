package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
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
	opts := openairesponses.EncodeOptions{
		ResponseID: "resp_encode_golden",
		MessageID:  "msg_encode_golden",
		CreatedAt:  1715620000,
	}
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	want := readGolden(t, "response_nonstream_expected.json")
	assertJSONEqual(t, want, rec.Body.Bytes())
}

func TestWriteNonStreamJSON_usageFromCollect(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 0},
		{Kind: lipapi.EventTextDelta, Delta: "hi"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 2},
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
	opts := openairesponses.EncodeOptions{
		ResponseID: "resp_usage_ns",
		MessageID:  "msg_usage_ns",
		CreatedAt:  1715620000,
	}
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage == nil || v.Usage.InputTokens != 9 || v.Usage.OutputTokens != 2 {
		t.Fatalf("usage %+v", v.Usage)
	}
}

func TestWriteErrorJSON_preOutputShape(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	openairesponses.WriteErrorJSON(rec, 400, "bad", "invalid_request_error", "empty")
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

func TestWriteStreamSSE_containsCompletedAndDone(t *testing.T) {
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_stream_ut", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "response.completed") {
		t.Fatalf("missing completed: %q", s)
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_stream_incr", MessageID: "msg_stream_incr", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var seq []string
	var doneText string
	var completedUsage struct {
		In  int `json:"input_tokens"`
		Out int `json:"output_tokens"`
	}
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		typ, _ := v["type"].(string)
		if typ == "" {
			continue
		}
		seq = append(seq, typ)
		if typ == "response.output_text.delta" {
			d, _ := v["delta"].(string)
			if d == "" {
				t.Fatalf("empty delta in %s", fr.Data)
			}
		}
		if typ == "response.output_text.done" {
			txt, _ := v["text"].(string)
			doneText = txt
		}
		if typ == "response.completed" {
			resp, _ := v["response"].(map[string]any)
			if resp == nil {
				t.Fatal("missing response")
			}
			if u, ok := resp["usage"].(map[string]any); ok {
				if x, ok := u["input_tokens"].(float64); ok {
					completedUsage.In = int(x)
				}
				if x, ok := u["output_tokens"].(float64); ok {
					completedUsage.Out = int(x)
				}
			}
		}
	}
	var deltaCount int
	for _, typ := range seq {
		if typ == "response.output_text.delta" {
			deltaCount++
		}
	}
	if deltaCount != 3 {
		t.Fatalf("delta count %d seq %v", deltaCount, seq)
	}
	if doneText != "hello world" {
		t.Fatalf("done text %q", doneText)
	}
	if completedUsage.In != 7 || completedUsage.Out != 3 {
		t.Fatalf("usage %+v", completedUsage)
	}
}

func mustModelExt(tb testing.TB, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{"openairesponses.model": raw}
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
