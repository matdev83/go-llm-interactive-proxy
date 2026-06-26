package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
)

func eventKinds(events []lipapi.Event) []lipapi.EventKind {
	out := make([]lipapi.EventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func TestChatCompletionEvents_textAndUsage(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_ns",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
  "usage": {
    "prompt_tokens": 1,
    "completion_tokens": 2,
    "total_tokens": 3,
    "completion_tokens_details": {"reasoning_tokens": 4}
  }
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	events := ChatCompletionEvents(comp)
	kinds := eventKinds(events)
	want := []lipapi.EventKind{
		lipapi.EventResponseStarted,
		lipapi.EventMessageStarted,
		lipapi.EventTextDelta,
		lipapi.EventUsageDelta,
		lipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("got kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event[%d] = %v, want %v", i, kinds[i], want[i])
		}
	}
	if events[2].Delta != "hello" {
		t.Fatalf("text delta = %q", events[2].Delta)
	}
	if events[3].ReasoningTokens != 4 || events[3].TotalTokens != 3 {
		t.Fatalf("usage event: %+v", events[3])
	}
}

func TestChatCompletionEvents_openRouterProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_or",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "openai/gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
  "usage": {
    "prompt_tokens": 3,
    "completion_tokens": 7,
    "total_tokens": 10,
    "prompt_tokens_details": {"cached_tokens": 1},
    "completion_tokens_details": {"reasoning_tokens": 5},
    "cost": 0.00014
  }
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	var usage lipapi.Event
	for _, ev := range ChatCompletionEvents(comp) {
		if ev.Kind == lipapi.EventUsageDelta {
			usage = ev
		}
	}
	if usage.Kind != lipapi.EventUsageDelta {
		t.Fatal("missing usage event")
	}
	if usage.CacheReadTokens != 1 || usage.ReasoningTokens != 5 {
		t.Fatalf("usage details: %+v", usage)
	}
	if usage.CostNanoUnits != 140_000 {
		t.Fatalf("CostNanoUnits = %d, want 140000", usage.CostNanoUnits)
	}
}

func TestChatCompletionEvents_toolCalls(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_tool_ns",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "tool_calls": [{
        "id": "call_ab",
        "type": "function",
        "function": {"name": "get_weather", "arguments": "{\"city\":\"NYC\"}"}
      }]
    },
    "finish_reason": "tool_calls"
  }]
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	events := ChatCompletionEvents(comp)
	var started, args, finished bool
	for _, ev := range events {
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			if ev.ToolCallID == "call_ab" && ev.ToolName == "get_weather" {
				started = true
			}
		case lipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_ab" && ev.Delta == `{"city":"NYC"}` {
				args = true
			}
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_ab" {
				finished = true
			}
		}
	}
	if !started || !args || !finished {
		t.Fatalf("tool events: started=%v args=%v finished=%v kinds=%v", started, args, finished, eventKinds(events))
	}
}

func TestChatCompletionEvents_noChoices(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_empty","object":"chat.completion","created":1715620000,"model":"gpt-4o","choices":[]}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}
	events := ChatCompletionEvents(comp)
	kinds := eventKinds(events)
	want := []lipapi.EventKind{lipapi.EventResponseStarted, lipapi.EventResponseFinished}
	if len(kinds) != len(want) {
		t.Fatalf("got %v, want %v", kinds, want)
	}
}

func TestChatCompletionEvents_reasoningField(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_reasoning",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "deepseek-r1",
  "choices": [{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}
	comp.Choices[0].Message.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning": respjson.NewField(`"think"`),
	}
	events := ChatCompletionEvents(comp)
	sawReasoning := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventReasoningDelta && ev.Delta == "think" {
			sawReasoning = true
		}
	}
	if !sawReasoning {
		t.Fatalf("expected EventReasoningDelta with 'think', got %v", eventKinds(events))
	}
}

func TestReasoningTextFromMessage_empty(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	if got := ReasoningTextFromMessage(msg); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningKey(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning": respjson.NewField(`"think step"`),
	}
	if got := ReasoningTextFromMessage(msg); got != "think step" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningContentKey(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning_content": respjson.NewField(`"deeper think"`),
	}
	if got := ReasoningTextFromMessage(msg); got != "deeper think" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_empty(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	if got := ReasoningTextFromChunkDelta(delta); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_reasoningKey(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	delta.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning": respjson.NewField(`"chunk think"`),
	}
	if got := ReasoningTextFromChunkDelta(delta); got != "chunk think" {
		t.Fatalf("got %q", got)
	}
}

func TestHandleChatChunk_toolCallsStreamingFromJSON(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	var args strings.Builder
	sawStarted := false
	sawFinished := false
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			if ev.ToolCallID == "call_ab" && ev.ToolName == "get_weather" {
				sawStarted = true
			}
		case lipapi.EventToolCallArgsDelta:
			args.WriteString(ev.Delta)
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_ab" {
				sawFinished = true
			}
		}
	}
	if !sawStarted {
		t.Fatal("expected ToolCallStarted for call_ab")
	}
	if !sawFinished {
		t.Fatal("expected ToolCallFinished for call_ab")
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("concat args: %q", got)
	}
}

func TestHandleChatChunk_toolCallArgsBufferedUntilID(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	events := stream.DrainPending(&s.pending)
	toolEvents := []lipapi.Event{}
	for _, ev := range events {
		if ev.Kind == lipapi.EventToolCallStarted || ev.Kind == lipapi.EventToolCallArgsDelta || ev.Kind == lipapi.EventToolCallFinished {
			toolEvents = append(toolEvents, ev)
		}
	}
	if len(toolEvents) != 4 {
		t.Fatalf("tool events = %+v", toolEvents)
	}
	if toolEvents[0].Kind != lipapi.EventToolCallStarted || toolEvents[0].ToolCallID != "call_ab" {
		t.Fatalf("start event = %+v", toolEvents[0])
	}
	if toolEvents[1].Kind != lipapi.EventToolCallArgsDelta || toolEvents[1].ToolCallID != "call_ab" || toolEvents[1].Delta != `{"city"` {
		t.Fatalf("buffered args event = %+v", toolEvents[1])
	}
	if toolEvents[2].Kind != lipapi.EventToolCallArgsDelta || toolEvents[2].ToolCallID != "call_ab" || toolEvents[2].Delta != `:"NYC"}` {
		t.Fatalf("current args event = %+v", toolEvents[2])
	}
	if toolEvents[3].Kind != lipapi.EventToolCallFinished || toolEvents[3].ToolCallID != "call_ab" {
		t.Fatalf("finish event = %+v", toolEvents[3])
	}
}

func TestHandleChatChunk_usageChunk(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_u","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}
	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	if err := s.handleChunk(ch); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(&s.pending)
	hasUsage := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			hasUsage = true
			if ev.InputTokens != 3 || ev.OutputTokens != 7 || ev.TotalTokens != 10 {
				t.Fatalf("usage event: %+v", ev)
			}
		}
	}
	if !hasUsage {
		t.Fatal("expected EventUsageDelta")
	}
}

func TestHandleChatChunk_reasoningContent(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_reasoning","object":"chat.completion.chunk","created":1715620000,"model":"deepseek-r1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"},"finish_reason":null}]}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	if err := s.handleChunk(ch); err != nil {
		t.Fatal(err)
	}

	events := stream.DrainPending(&s.pending)
	for _, ev := range events {
		if ev.Kind == lipapi.EventReasoningDelta && ev.Delta == "thinking" {
			return
		}
	}
	t.Fatalf("expected reasoning delta, got %v", eventKinds(events))
}
