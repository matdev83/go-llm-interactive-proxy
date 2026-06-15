package openrouter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func TestHandleChunk_toolCallsStreamingFromJSON(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for i, raw := range chunks {
		t.Run("chunk_"+string(rune('0'+i)), func(t *testing.T) {
			var ch openai.ChatCompletionChunk
			if err := json.Unmarshal([]byte(raw), &ch); err != nil {
				t.Fatal(err)
			}
			if err := s.handleChunk(ch); err != nil {
				t.Fatal(err)
			}
		})
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

func TestHandleUnion_streamError_emitsEventError(t *testing.T) {
	t.Parallel()
	raw := `{"type":"error","code":"invalid_request_error","message":"bad request","sequence_number":2}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}

	s := newUnitResponsesStream()
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	sawResponseStarted := false
	sawError := false
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventResponseStarted {
			sawResponseStarted = true
		}
		if ev.Kind == lipapi.EventError {
			sawError = true
			if ev.ErrorCode != "invalid_request_error" || ev.ErrorMessage != "bad request" {
				t.Fatalf("error event: %+v", ev)
			}
		}
	}
	if !sawResponseStarted {
		t.Fatal("expected EventResponseStarted before stream error")
	}
	if !sawError {
		t.Fatal("expected EventError")
	}
}

func TestHandleUnion_toolCallStream_mapsToolEvents(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()

	rawEvents := []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","status":"in_progress","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"\"NYC\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`,
	}
	for _, raw := range rawEvents {
		var u responses.ResponseStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if err := s.handleUnion(u); err != nil {
			t.Fatal(err)
		}
	}

	var kinds []lipapi.EventKind
	var args strings.Builder
	for _, ev := range stream.DrainPending(&s.pending) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == lipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if len(kinds) < 4 {
		t.Fatalf("unexpected events: %v", kinds)
	}
	if kinds[0] != lipapi.EventResponseStarted || kinds[1] != lipapi.EventMessageStarted || kinds[2] != lipapi.EventToolCallStarted {
		t.Fatalf("opening events: %v", kinds)
	}
	if kinds[len(kinds)-1] != lipapi.EventToolCallFinished {
		t.Fatalf("last event: %v", kinds[len(kinds)-1])
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("combined args: %q", got)
	}
}

func TestHandleUnion_completed_emitsAssistantMedia(t *testing.T) {
	t.Parallel()
	raw := `{
  "type": "response.completed",
  "sequence_number": 1,
  "response": {
    "id": "resp_x",
    "object": "response",
    "status": "completed",
    "model": "gpt-4o-mini",
    "output": [
      {
        "type": "message",
        "id": "m1",
        "status": "completed",
        "role": "assistant",
        "content": [
          {"type": "output_text", "text": "see"},
          {"type": "input_image", "image_url": {"url": "https://img.example/x.png"}},
          {"type": "input_file", "file_id": "file_doc_1"}
        ]
      }
    ]
  }
}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}

	s := newUnitResponsesStream()
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	gotImage := ""
	gotFile := ""
	sawFinished := false
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case lipapi.EventAssistantImageRef:
			gotImage = ev.AssistantRef
		case lipapi.EventAssistantFileRef:
			gotFile = ev.AssistantRef
		case lipapi.EventResponseFinished:
			sawFinished = true
		}
	}
	if gotImage != "https://img.example/x.png" {
		t.Fatalf("image ref: %q", gotImage)
	}
	if gotFile != "file_doc_1" {
		t.Fatalf("file ref: %q", gotFile)
	}
	if !sawFinished {
		t.Fatal("expected EventResponseFinished")
	}
}

func newUnitResponsesStream() *responsesStream {
	return &responsesStream{
		pending:           stream.NewPendingEventQueue(0),
		toolCallStarted:   map[string]bool{},
		toolCallArgDeltas: map[string]bool{},
		toolCallFinished:  map[string]bool{},
	}
}
