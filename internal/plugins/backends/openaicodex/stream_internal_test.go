package openaicodex

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestHandleData_malformedJSON_returnsError(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)
	if err := s.handleData("{not json"); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestRecv_malformedSSEJSON_returnsError(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("data: {broken\n"))
	s := newCodexStream(body, 64)
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestHandleData_responseCreatedAndCompleted_mapsLifecycleAndUsage(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)

	created := `{"type":"response.created","response":{"id":"resp_created"}}`
	completed := `{"type":"response.completed","response":{"id":"resp_completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`
	for _, raw := range []string{created, completed} {
		if err := s.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}
	events := stream.DrainPending(&s.pending)
	want := []lipapi.EventKind{lipapi.EventResponseStarted, lipapi.EventMessageStarted, lipapi.EventUsageDelta, lipapi.EventResponseFinished}
	if len(events) != len(want) {
		t.Fatalf("events: %+v", events)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event[%d] = %v, want %v", i, events[i].Kind, kind)
		}
	}
	if usage := events[2]; usage.InputTokens != 1 || usage.OutputTokens != 2 || usage.TotalTokens != 3 {
		t.Fatalf("usage: %+v", usage)
	}
}

func TestHandleData_toolCallStream_mapsToCanonicalToolEvents(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)

	rawEvents := []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","status":"in_progress","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"\"NYC\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`,
	}
	for _, raw := range rawEvents {
		if err := s.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
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
	if kinds[0] != lipapi.EventResponseStarted || kinds[1] != lipapi.EventMessageStarted || kinds[2] != lipapi.EventToolCallStarted {
		t.Fatalf("opening events: %v", kinds)
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("combined args: %q", got)
	}
	if kinds[len(kinds)-1] != lipapi.EventToolCallFinished {
		t.Fatalf("last event: %v", kinds)
	}
}

func TestHandleData_completedOnly_emitsFullText(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)

	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`
	if err := s.handleData(completed); err != nil {
		t.Fatal(err)
	}

	var texts []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if len(texts) != 1 || texts[0] != "done" {
		t.Fatalf("texts: %v", texts)
	}
}

func TestHandleData_completed_replaysFunctionCalls(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)

	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}]}}`
	if err := s.handleData(completed); err != nil {
		t.Fatal(err)
	}

	var kinds []lipapi.EventKind
	for _, ev := range stream.DrainPending(&s.pending) {
		kinds = append(kinds, ev.Kind)
	}
	want := []lipapi.EventKind{
		lipapi.EventResponseStarted,
		lipapi.EventMessageStarted,
		lipapi.EventToolCallStarted,
		lipapi.EventToolCallArgsDelta,
		lipapi.EventToolCallFinished,
		lipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events: %v", kinds)
	}
	for i, kind := range want {
		if kinds[i] != kind {
			t.Fatalf("event[%d] = %v, want %v", i, kinds[i], kind)
		}
	}
}

func TestHandleData_outputItemDone_emitsCompleteToolCall(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)

	raw := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_done","call_id":"call_fc_done","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}`
	if err := s.handleData(raw); err != nil {
		t.Fatal(err)
	}

	var kinds []lipapi.EventKind
	for _, ev := range stream.DrainPending(&s.pending) {
		kinds = append(kinds, ev.Kind)
	}
	want := []lipapi.EventKind{
		lipapi.EventResponseStarted,
		lipapi.EventMessageStarted,
		lipapi.EventToolCallStarted,
		lipapi.EventToolCallArgsDelta,
		lipapi.EventToolCallFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events: %v", kinds)
	}
	for i, kind := range want {
		if kinds[i] != kind {
			t.Fatalf("event[%d] = %v, want %v", i, kinds[i], kind)
		}
	}
}

func TestHandleData_toolCallStream_callIDOnDelta(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("")), 64)

	rawEvents := []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","call_id":"call_only","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"call_id":"call_only","output_index":0,"delta":"{\"x\":1}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":2,"call_id":"call_only","output_index":0,"name":"get_weather","arguments":"{\"x\":1}"}`,
	}
	for _, raw := range rawEvents {
		if err := s.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}

	var toolID string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventToolCallStarted {
			toolID = ev.ToolCallID
		}
	}
	if toolID != "call_only" {
		t.Fatalf("tool call id: %q", toolID)
	}
}
