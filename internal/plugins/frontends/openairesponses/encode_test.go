package openairesponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
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

func TestWriteNonStreamJSON_defaultsAreDeterministic(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "stable"},
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
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, es, openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		Output    []struct {
			ID string `json:"id"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	wantID := "resp_" + diag.StableCallToken(call)
	if v.ID != wantID {
		t.Fatalf("response id %q, want %q", v.ID, wantID)
	}
	if v.CreatedAt != diag.StableUnix(call) {
		t.Fatalf("created_at %d, want %d", v.CreatedAt, diag.StableUnix(call))
	}
	if len(v.Output) == 0 || v.Output[0].ID != "msg_"+wantID {
		t.Fatalf("message id = %+v", v.Output)
	}
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

func TestWriteNonStreamJSON_functionCallOutput(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "fn1"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"z":2}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: lipapi.EventTextDelta, Delta: "t"},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_tool_ns", MessageID: "msg_tool_ns", CreatedAt: 1715620000}
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Output) < 2 {
		t.Fatalf("output: %+v", v.Output)
	}
	if v.Output[1]["type"] != "function_call" || v.Output[1]["name"] != "fn1" {
		t.Fatalf("fc: %+v", v.Output[1])
	}
}

func TestWriteStreamSSE_reasoningDeltaDoesNotBreakCompletion(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "think-step"},
		{Kind: lipapi.EventTextDelta, Delta: "answer"},
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
	if err := openairesponses.WriteStreamSSE(context.Background(), rec, call, es, openairesponses.EncodeOptions{ResponseID: "resp_re", CreatedAt: 1715620000}); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if strings.Contains(s, "think-step") {
		t.Fatalf("reasoning must not appear on Responses SSE wire in v1 subset; body=%q", s)
	}
	if !strings.Contains(s, "answer") || !strings.Contains(s, "response.completed") {
		t.Fatalf("expected normal text completion; body=%q", s)
	}
}

func TestWriteStreamSSE_toolCallEvents(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "pre"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "fn_a"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"ci`},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `ty":"ny"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: lipapi.EventTextDelta, Delta: "post"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 5, OutputTokens: 8},
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
	opts := openairesponses.EncodeOptions{ResponseID: "resp_tc", MessageID: "msg_tc", CreatedAt: 1715620000}
	if err := openairesponses.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var argDeltas, addedTypes []string
	var doneArgs string
	var completedFC map[string]any
	for _, fr := range frames {
		if fr.Data == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		typ, _ := v["type"].(string)
		switch typ {
		case "response.output_item.added":
			item, _ := v["item"].(map[string]any)
			if item != nil && item["type"] == "function_call" {
				addedTypes = append(addedTypes, "function_call")
			}
		case "response.function_call_arguments.delta":
			d, _ := v["delta"].(string)
			argDeltas = append(argDeltas, d)
		case "response.function_call_arguments.done":
			doneArgs, _ = v["arguments"].(string)
		case "response.completed":
			resp, _ := v["response"].(map[string]any)
			out, _ := resp["output"].([]any)
			for _, item := range out {
				m, _ := item.(map[string]any)
				if m != nil && m["type"] == "function_call" {
					completedFC = m
				}
			}
		}
	}
	if len(addedTypes) == 0 || addedTypes[0] != "function_call" {
		t.Fatalf("expected function_call output_item.added, got %v", addedTypes)
	}
	if len(argDeltas) != 2 || argDeltas[0] != `{"ci` || argDeltas[1] != `ty":"ny"}` {
		t.Fatalf("arg deltas: %#v", argDeltas)
	}
	if doneArgs != `{"city":"ny"}` {
		t.Fatalf("done args: %q", doneArgs)
	}
	if completedFC == nil || completedFC["name"] != "fn_a" || completedFC["status"] != "completed" {
		t.Fatalf("completed function_call: %+v", completedFC)
	}
}

func TestWriteNonStreamJSON_messageContentIncludesAssistantImageRef(t *testing.T) {
	t.Parallel()
	es := lipapi.FixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "x"},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "https://example.com/p.png"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "a:b"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("u")}}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
	}
	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, es, openairesponses.EncodeOptions{CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Output) < 1 {
		t.Fatal("missing output")
	}
	var msg struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body.Output[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "message" {
		t.Fatalf("type %q", msg.Type)
	}
	var content []map[string]any
	if err := json.Unmarshal(msg.Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 {
		t.Fatalf("content len %d: %v", len(content), content)
	}
	if content[0]["type"] != "output_text" || content[0]["text"] != "x" {
		t.Fatalf("part0: %#v", content[0])
	}
	if content[1]["type"] != "input_image" || content[1]["image_url"] != "https://example.com/p.png" {
		t.Fatalf("part1: %#v", content[1])
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
