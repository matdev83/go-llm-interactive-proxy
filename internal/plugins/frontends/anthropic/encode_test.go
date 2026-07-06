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

func TestWriteStreamSSE_thinkingBlock(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
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
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var thinkingStartIdx *int
	var thinkingDeltas []string
	var thinkingStopSeen bool
	var sawTextBeforeThinkingStop bool
	var textDeltaSeen bool
	for _, fr := range frames {
		switch fr.Event {
		case "content_block_start":
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "thinking" {
				idx := v.Index
				thinkingStartIdx = &idx
			}
		case "content_block_delta":
			if !thinkingStopSeen {
				var probe struct {
					Delta struct {
						Type string `json:"type"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(fr.Data), &probe); err != nil {
					t.Fatal(err)
				}
				if probe.Delta.Type == "text_delta" {
					sawTextBeforeThinkingStop = true
				}
			}
			var v struct {
				Index int `json:"index"`
				Delta struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
					Text     string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "thinking_delta" {
				thinkingDeltas = append(thinkingDeltas, v.Delta.Thinking)
			}
			if v.Delta.Type == "text_delta" {
				textDeltaSeen = true
			}
		case "content_block_stop":
			var v struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if thinkingStartIdx != nil && v.Index == *thinkingStartIdx {
				thinkingStopSeen = true
			}
		}
	}
	if thinkingStartIdx == nil {
		t.Fatal("missing thinking content_block_start")
	}
	if len(thinkingDeltas) != 1 || thinkingDeltas[0] != "plan" {
		t.Fatalf("thinking deltas: %#v", thinkingDeltas)
	}
	if !thinkingStopSeen {
		t.Fatal("missing thinking content_block_stop")
	}
	if sawTextBeforeThinkingStop {
		t.Fatal("text_delta arrived before thinking block stopped")
	}
	if !textDeltaSeen {
		t.Fatal("missing text_delta after thinking block")
	}
}

// parseThinkingTransition inspects SSE frames for a thinking block followed by
// another content block. It returns the 0-based frame positions of the thinking
// content_block_start, its matching content_block_stop, and the first
// subsequent non-thinking content_block_start, plus the thinking and next block
// indices. -1 means the corresponding event was not found.
func parseThinkingTransition(t *testing.T, frames []testkit.SSEFrame) (thinkStartFrame, thinkStopFrame, nextStartFrame, thinkIdx, nextIdx int) {
	t.Helper()
	thinkStartFrame, thinkStopFrame, nextStartFrame = -1, -1, -1
	for i, fr := range frames {
		switch fr.Event {
		case "content_block_start":
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "thinking" {
				if thinkStartFrame == -1 {
					thinkStartFrame = i
					thinkIdx = v.Index
				}
			} else if thinkStartFrame != -1 && nextStartFrame == -1 {
				nextStartFrame = i
				nextIdx = v.Index
			}
		case "content_block_stop":
			var v struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if thinkStartFrame != -1 && v.Index == thinkIdx && thinkStopFrame == -1 {
				thinkStopFrame = i
			}
		}
	}
	return
}

func TestWriteStreamSSE_thinkingBlockThenTool(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "tu_rt", ToolName: "lookup"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "tu_rt", Delta: `{"q":"go"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "tu_rt"},
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
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_tool"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	thinkStart, thinkStop, nextStart, thinkIdx, toolIdx := parseThinkingTransition(t, frames)
	if thinkStart == -1 {
		t.Fatal("missing thinking content_block_start")
	}
	if thinkStop == -1 {
		t.Fatal("missing thinking content_block_stop")
	}
	if nextStart == -1 {
		t.Fatal("missing tool content_block_start after thinking")
	}
	if thinkStop >= nextStart {
		t.Fatalf("thinking content_block_stop (frame %d) must precede tool content_block_start (frame %d)", thinkStop, nextStart)
	}
	if thinkIdx == toolIdx {
		t.Fatalf("thinking and tool blocks share index %d", thinkIdx)
	}
	var toolDeltas []int
	for _, fr := range frames {
		if fr.Event != "content_block_delta" {
			continue
		}
		var v struct {
			Index int `json:"index"`
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Delta.Type == "input_json_delta" {
			toolDeltas = append(toolDeltas, v.Index)
		}
	}
	if len(toolDeltas) == 0 {
		t.Fatal("missing input_json_delta")
	}
	for _, idx := range toolDeltas {
		if idx != toolIdx {
			t.Fatalf("input_json_delta index %d != tool block index %d", idx, toolIdx)
		}
	}
}

func TestWriteStreamSSE_thinkingBlockThenMedia(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventAssistantImageRef, AssistantRef: "https://img.example/x.png", AssistantMIME: "image/png"},
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
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_media"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	thinkStart, thinkStop, nextStart, thinkIdx, mediaIdx := parseThinkingTransition(t, frames)
	if thinkStart == -1 {
		t.Fatal("missing thinking content_block_start")
	}
	if thinkStop == -1 {
		t.Fatal("missing thinking content_block_stop")
	}
	if nextStart == -1 {
		t.Fatal("missing media content_block_start after thinking")
	}
	if thinkStop >= nextStart {
		t.Fatalf("thinking content_block_stop (frame %d) must precede media content_block_start (frame %d)", thinkStop, nextStart)
	}
	if thinkIdx == mediaIdx {
		t.Fatalf("thinking and media blocks share index %d", thinkIdx)
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

func TestWriteStreamSSE_thinkingSignatureEmitted(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-plan"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_sig"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var thinkingIdx int
	var gotStartSignature string
	signatureDeltaIdx := -1
	var signatureDeltaValue string
	var signatureDeltaStopOrder []string
	for _, fr := range frames {
		switch fr.Event {
		case "content_block_start":
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type      string `json:"type"`
					Signature string `json:"signature"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "thinking" {
				thinkingIdx = v.Index
				gotStartSignature = v.ContentBlock.Signature
			}
		case "content_block_delta":
			var v struct {
				Index int `json:"index"`
				Delta struct {
					Type      string `json:"type"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "signature_delta" {
				signatureDeltaIdx = v.Index
				signatureDeltaValue = v.Delta.Signature
			}
		case "content_block_stop":
			var v struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Index == thinkingIdx {
				signatureDeltaStopOrder = append(signatureDeltaStopOrder, "stop")
			}
		}
		if fr.Event == "content_block_delta" {
			var probe struct {
				Delta struct {
					Type string `json:"type"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &probe); err != nil {
				t.Fatal(err)
			}
			if probe.Delta.Type == "signature_delta" {
				signatureDeltaStopOrder = append(signatureDeltaStopOrder, "sig")
			}
		}
	}
	if gotStartSignature != "" {
		t.Fatalf("thinking content_block_start signature should be empty on start, got %q", gotStartSignature)
	}
	if signatureDeltaIdx == -1 {
		t.Fatal("missing signature_delta")
	}
	if signatureDeltaIdx != thinkingIdx {
		t.Fatalf("signature_delta index %d != thinking block index %d", signatureDeltaIdx, thinkingIdx)
	}
	if signatureDeltaValue != "sig-plan" {
		t.Fatalf("signature_delta value %q, want sig-plan", signatureDeltaValue)
	}
	if len(signatureDeltaStopOrder) < 2 || signatureDeltaStopOrder[len(signatureDeltaStopOrder)-1] != "stop" || !slices.Contains(signatureDeltaStopOrder[:len(signatureDeltaStopOrder)-1], "sig") {
		t.Fatalf("signature_delta must precede thinking content_block_stop; order: %v", signatureDeltaStopOrder)
	}
}

func TestWriteStreamSSE_thinkingSignatureAccumulated(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-chunk-1"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-chunk-2"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_sig_acc"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var signatureDeltas []string
	for _, fr := range frames {
		if fr.Event != "content_block_delta" {
			continue
		}
		var v struct {
			Delta struct {
				Type      string `json:"type"`
				Signature string `json:"signature"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Delta.Type == "signature_delta" {
			signatureDeltas = append(signatureDeltas, v.Delta.Signature)
		}
	}
	if len(signatureDeltas) != 1 {
		t.Fatalf("want exactly one signature_delta, got %v", signatureDeltas)
	}
	if signatureDeltas[0] != "sig-chunk-1sig-chunk-2" {
		t.Fatalf("signature_delta value %q, want concatenated signature", signatureDeltas[0])
	}
}

func TestWriteStreamSSE_thinkingSignatureNoSignatureEvent(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan"},
		{Kind: lipapi.EventTextDelta, Delta: "ans"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_nosig"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	for _, fr := range frames {
		if fr.Event != "content_block_delta" {
			continue
		}
		var v struct {
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatal(err)
		}
		if v.Delta.Type == "signature_delta" {
			t.Fatal("unexpected signature_delta when no EventReasoningSignatureDelta arrived")
		}
	}
}

func TestWriteStreamSSE_thinkingSignatureMultiBlock(t *testing.T) {
	t.Parallel()
	es := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan-a"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-a"},
		{Kind: lipapi.EventTextDelta, Delta: "bridge"},
		{Kind: lipapi.EventReasoningDelta, Delta: "plan-b"},
		{Kind: lipapi.EventReasoningSignatureDelta, Signature: "sig-b"},
		{Kind: lipapi.EventResponseFinished},
	})
	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "x:y"},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("p")}}},
		Extensions: mustModelExt(t, "claude-3-5-haiku-20241022"),
	}
	rec := httptest.NewRecorder()
	if err := anthropic.WriteStreamSSE(context.Background(), rec, call, es, anthropic.EncodeOptions{MessageID: "msg_thinking_multi"}); err != nil {
		t.Fatal(err)
	}
	frames := testkit.ParseRecorderSSE(rec)
	var thinkingIdx []int
	var sigDeltas []struct {
		Index     int
		Signature string
	}
	for _, fr := range frames {
		switch fr.Event {
		case "content_block_start":
			var v struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.ContentBlock.Type == "thinking" {
				thinkingIdx = append(thinkingIdx, v.Index)
			}
		case "content_block_delta":
			var v struct {
				Index int `json:"index"`
				Delta struct {
					Type      string `json:"type"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
				t.Fatal(err)
			}
			if v.Delta.Type == "signature_delta" {
				sigDeltas = append(sigDeltas, struct {
					Index     int
					Signature string
				}{Index: v.Index, Signature: v.Delta.Signature})
			}
		}
	}
	if len(thinkingIdx) != 2 {
		t.Fatalf("want 2 thinking blocks, got %v", thinkingIdx)
	}
	if len(sigDeltas) != 2 {
		t.Fatalf("want 2 signature_delta events, got %+v", sigDeltas)
	}
	for i, sig := range sigDeltas {
		wantSig := []string{"sig-a", "sig-b"}[i]
		wantIdx := thinkingIdx[i]
		if sig.Index != wantIdx {
			t.Fatalf("signature_delta %d index %d != thinking block index %d", i, sig.Index, wantIdx)
		}
		if sig.Signature != wantSig {
			t.Fatalf("signature_delta %d value %q, want %q", i, sig.Signature, wantSig)
		}
	}
}
