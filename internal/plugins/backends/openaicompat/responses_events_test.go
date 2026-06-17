package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/responses"
)

func TestResponsesStreamFinishOnEOF_afterResponseStarted(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	s.sawResp = true

	if err := s.finishOnEOF(); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(&s.pending)
	if len(events) != 1 || events[0].Kind != lipapi.EventResponseFinished {
		t.Fatalf("events = %+v", events)
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

func TestHandleUnion_outputItemDone_emitsCompleteToolCall(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()

	raw := `{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_done","call_id":"call_fc_done","status":"completed","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	kinds := eventKinds(stream.DrainPending(&s.pending))
	want := []lipapi.EventKind{
		lipapi.EventResponseStarted,
		lipapi.EventMessageStarted,
		lipapi.EventToolCallStarted,
		lipapi.EventToolCallArgsDelta,
		lipapi.EventToolCallFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event[%d] = %v, want %v", i, kinds[i], want[i])
		}
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

func TestResponseEvents_textAndUsage(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "resp_ns",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "hello"}
      ]
    }
  ],
  "usage": {
    "input_tokens": 1,
    "output_tokens": 2,
    "total_tokens": 3,
    "output_tokens_details": {"reasoning_tokens": 4}
  }
}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}

	events, err := ResponseEvents(resp)
	if err != nil {
		t.Fatal(err)
	}
	kinds := eventKinds(events)
	want := []lipapi.EventKind{
		lipapi.EventResponseStarted,
		lipapi.EventMessageStarted,
		lipapi.EventTextDelta,
		lipapi.EventUsageDelta,
		lipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
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
