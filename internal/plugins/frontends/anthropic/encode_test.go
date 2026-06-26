package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestWriteNonStreamJSON_toolUseBlock(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "toolu_test", ToolName: "alpha"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "toolu_test", Delta: `{"k":1}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "toolu_test"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, call, es, anthropic.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			ID    string          `json:"id"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.StopReason != "tool_use" {
		t.Fatalf("stop %q", v.StopReason)
	}
	if len(v.Content) != 1 || v.Content[0].Type != "tool_use" || v.Content[0].Name != "alpha" {
		t.Fatalf("content %+v", v.Content)
	}
}

func TestWriteNonStreamJSONUsesClientVisibleScopedUsage(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{
			{InputTokens: 100, OutputTokens: 50, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable}},
			{InputTokens: 10, OutputTokens: 5, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneClientVisible}},
		}},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()

	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, &lipapi.Call{Extensions: mustModelExt(t, "claude-3-5-haiku-20241022")}, es, anthropic.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want client-visible 10/5", got.Usage)
	}
}

func TestWriteNonStreamJSON_defaultsAreDeterministic(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
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
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, call, es, anthropic.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	wantID := "msg_" + diag.StableCallToken(call)
	if v.ID != wantID {
		t.Fatalf("message id %q, want %q", v.ID, wantID)
	}
	if v.StopReason != "end_turn" {
		t.Fatalf("stop_reason %q", v.StopReason)
	}
}

func mustModelExt(tb testing.TB, model string) map[string]json.RawMessage {
	tb.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		tb.Fatal(err)
	}
	return map[string]json.RawMessage{"anthropic.model": raw}
}

func TestWriteNonStreamJSON_shape(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
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
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_encode_ut"}
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var v struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.ID != "msg_encode_ut" {
		t.Fatalf("id %q", v.ID)
	}
	if v.Type != "message" || v.Role != "assistant" {
		t.Fatalf("type/role: %+v", v)
	}
	if v.Model != "claude-3-5-haiku-20241022" {
		t.Fatalf("model %q", v.Model)
	}
	if len(v.Content) != 1 || v.Content[0].Type != "text" || v.Content[0].Text != "golden-ok" {
		t.Fatalf("content: %+v", v.Content)
	}
	if v.StopReason != "end_turn" {
		t.Fatal(v.StopReason)
	}
}

func TestWriteNonStreamJSON_usageFromCollect(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 0},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 0, OutputTokens: 5},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_usage_ut"}
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Usage.InputTokens != 11 || v.Usage.OutputTokens != 5 {
		t.Fatalf("usage %+v", v.Usage)
	}
}

func TestWriteErrorJSON_shape(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if err := anthropic.WriteErrorJSON(rec, 400, "bad", "invalid_request_error"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	var v struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Type != "error" || v.Error.Type != "invalid_request_error" || v.Error.Message != "bad" {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestWriteStreamSSE_eventsAndText(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
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
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_stream_ut"}
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "event: message_start") {
		t.Fatalf("missing message_start: %q", s)
	}
	if !strings.Contains(s, "event: message_stop") {
		t.Fatalf("missing message_stop: %q", s)
	}
	if !strings.Contains(s, "stream-ok") {
		t.Fatalf("missing text: %q", s)
	}
	if !strings.Contains(s, "content_block_delta") {
		t.Fatalf("missing delta: %q", s)
	}
}

func TestWriteStreamSSE_incrementalTextDeltas(t *testing.T) {
	t.Parallel()
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
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_stream_incr"}
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var deltaTexts []string
	var msgStartIn, msgDeltaOut int
	for _, fr := range frames {
		if fr.Event == "content_block_delta" {
			var v struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "text_delta" {
				deltaTexts = append(deltaTexts, v.Delta.Text)
			}
		}
		if fr.Event == "message_start" {
			var v struct {
				Message struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			msgStartIn = v.Message.Usage.InputTokens
		}
		if fr.Event == "message_delta" {
			var v struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			msgDeltaOut = v.Usage.OutputTokens
		}
	}
	if len(deltaTexts) != 3 || deltaTexts[0] != "hel" || deltaTexts[1] != "lo" || deltaTexts[2] != " world" {
		t.Fatalf("delta texts: %#v", deltaTexts)
	}
	if msgStartIn != 7 {
		t.Fatalf("message_start input_tokens got %d", msgStartIn)
	}
	if msgDeltaOut != 3 {
		t.Fatalf("message_delta output_tokens got %d", msgDeltaOut)
	}
}

func TestWriteStreamSSE_toolUseBlock(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "pre"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "tu_1", ToolName: "lookup"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "tu_1", Delta: `{"q":"go`},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "tu_1", Delta: `"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "tu_1"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_tool_sse"}
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var toolBlockStarts, inputDeltas, blockStops []int
	var stopReason string
	for _, fr := range frames {
		if fr.Event == "content_block_start" {
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					Name string `json:"name"`
					ID   string `json:"id"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "tool_use" {
				toolBlockStarts = append(toolBlockStarts, v.Index)
				if v.ContentBlock.Name != "lookup" {
					t.Fatalf("tool name: %q", v.ContentBlock.Name)
				}
				if v.ContentBlock.ID != "tu_1" {
					t.Fatalf("tool id: %q", v.ContentBlock.ID)
				}
			}
		}
		if fr.Event == "content_block_delta" {
			var v struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "input_json_delta" {
				inputDeltas = append(inputDeltas, v.Index)
			}
		}
		if fr.Event == "content_block_stop" {
			var v struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			blockStops = append(blockStops, v.Index)
		}
		if fr.Event == "message_delta" {
			var v struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			stopReason = v.Delta.StopReason
		}
	}
	if len(toolBlockStarts) != 1 {
		t.Fatalf("tool_use block starts: %v", toolBlockStarts)
	}
	if len(inputDeltas) != 2 {
		t.Fatalf("input_json_delta count: %d", len(inputDeltas))
	}
	if len(blockStops) != 2 {
		t.Fatalf("content_block_stop count: %d indices=%v", len(blockStops), blockStops)
	}
	toolIdx := toolBlockStarts[0]
	if !slices.Contains(blockStops, toolIdx) {
		t.Fatalf("expected content_block_stop for tool block index %d, got %v", toolIdx, blockStops)
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop_reason: %q", stopReason)
	}
}

func TestWriteNonStreamJSON_toolUseOutput(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "tu_2", ToolName: "search"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "tu_2", Delta: `{"k":"v"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "tu_2"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "x:y"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")},
		}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_tool_ns"}
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			ID    string          `json:"id"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.StopReason != "tool_use" {
		t.Fatalf("stop_reason: %q", v.StopReason)
	}
	var sawTool bool
	for _, c := range v.Content {
		if c.Type == "tool_use" && c.Name == "search" && c.ID == "tu_2" {
			sawTool = true
			if !strings.Contains(string(c.Input), `"k"`) {
				t.Fatalf("input: %s", string(c.Input))
			}
		}
	}
	if !sawTool {
		t.Fatalf("content: %+v", v.Content)
	}
}

func TestWriteStreamSSE_usageDetails_defaultOmitsLipExtensions(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_stream_usage_default"}); err != nil {
		t.Fatal(err)
	}
	var msgDeltaUsage map[string]any
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Event != "message_delta" {
			continue
		}
		var v struct {
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		msgDeltaUsage = v.Usage
	}
	if msgDeltaUsage == nil {
		t.Fatal("missing message_delta usage")
	}
	if msgDeltaUsage["output_tokens"] != float64(20) {
		t.Fatalf("output_tokens: %+v", msgDeltaUsage)
	}
	if msgDeltaUsage["cache_read_input_tokens"] != float64(30) || msgDeltaUsage["cache_creation_input_tokens"] != float64(5) {
		t.Fatalf("native cache fields: %+v", msgDeltaUsage)
	}
	for _, key := range []string{"x_lip_cost_nano_units", "x_lip_currency", "x_lip_cost_source", "x_lip_uncached_tokens"} {
		if _, ok := msgDeltaUsage[key]; ok {
			t.Fatalf("unexpected %q in default stream usage: %+v", key, msgDeltaUsage)
		}
	}
}

func TestWriteStreamSSE_usageDetails_exposesLipExtensionsWhenConfigured(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5, CostNanoUnits: 12345, Currency: "USD", CostSource: "provider"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{MessageID: "msg_stream_usage_ext", ExposeLipUsageExtensions: true}
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, opts); err != nil {
		t.Fatal(err)
	}
	var msgDeltaUsage map[string]any
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Event != "message_delta" {
			continue
		}
		var v struct {
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		msgDeltaUsage = v.Usage
	}
	if msgDeltaUsage == nil {
		t.Fatal("missing message_delta usage")
	}
	if msgDeltaUsage["cache_read_input_tokens"] != float64(30) || msgDeltaUsage["cache_creation_input_tokens"] != float64(5) {
		t.Fatalf("native cache fields: %+v", msgDeltaUsage)
	}
	if msgDeltaUsage["x_lip_cost_nano_units"] != float64(12345) || msgDeltaUsage["x_lip_currency"] != "USD" || msgDeltaUsage["x_lip_cost_source"] != "provider" {
		t.Fatalf("cost extensions: %+v", msgDeltaUsage)
	}
	if msgDeltaUsage["x_lip_uncached_tokens"] != float64(70) {
		t.Fatalf("uncached tokens: %+v", msgDeltaUsage)
	}
}

func TestWriteNonStreamJSON_usageDetails_defaultOmitsLipExtensions(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, &lipapi.Call{Extensions: mustModelExt(t, "claude-3-5-haiku-20241022")}, es, anthropic.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	usage := testkit.MustMapStringAny(t, raw["usage"])
	if usage["input_tokens"] != float64(100) || usage["output_tokens"] != float64(20) {
		t.Fatalf("usage tokens: %+v", usage)
	}
	if usage["cache_read_input_tokens"] != float64(30) || usage["cache_creation_input_tokens"] != float64(5) {
		t.Fatalf("native cache fields: %+v", usage)
	}
	for _, key := range []string{"x_lip_cost_nano_units", "x_lip_currency", "x_lip_cost_source", "x_lip_uncached_tokens"} {
		if _, ok := usage[key]; ok {
			t.Fatalf("unexpected %q in default usage: %+v", key, usage)
		}
	}
}

func TestWriteNonStreamJSON_usageDetails_exposesLipExtensionsWhenConfigured(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventUsageDelta, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5, CostNanoUnits: 12345, Currency: "USD", CostSource: "provider"},
		{Kind: lipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	opts := anthropic.EncodeOptions{ExposeLipUsageExtensions: true}
	if err := anthropic.WriteNonStreamJSON(context.Background(), rec, &lipapi.Call{Extensions: mustModelExt(t, "claude-3-5-haiku-20241022")}, es, opts); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	usage := testkit.MustMapStringAny(t, raw["usage"])
	if usage["cache_read_input_tokens"] != float64(30) || usage["cache_creation_input_tokens"] != float64(5) {
		t.Fatalf("native cache fields: %+v", usage)
	}
	if usage["x_lip_cost_nano_units"] != float64(12345) || usage["x_lip_currency"] != "USD" || usage["x_lip_cost_source"] != "provider" {
		t.Fatalf("cost extensions: %+v", usage)
	}
	if usage["x_lip_uncached_tokens"] != float64(70) {
		t.Fatalf("uncached tokens: %+v", usage)
	}
}
