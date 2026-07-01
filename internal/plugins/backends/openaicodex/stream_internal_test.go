package openaicodex

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testCodexStream() *codexStream {
	return newCodexStream(io.NopCloser(strings.NewReader("")), 64)
}

func TestHandleData_malformedJSON_returnsError(t *testing.T) {
	t.Parallel()
	s := testCodexStream()
	if err := s.mapper.handleData("{not json"); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestRecv_malformedSSEJSON_returnsError(t *testing.T) {
	t.Parallel()
	s := newCodexStream(io.NopCloser(strings.NewReader("data: {broken\n")), 64)
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestHandleData_responseCreatedAndCompleted_mapsLifecycleAndUsage(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	created := `{"type":"response.created","response":{"id":"resp_created"}}`
	completed := `{"type":"response.completed","response":{"id":"resp_completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`
	for _, raw := range []string{created, completed} {
		if err := s.mapper.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}
	events := stream.DrainPending(&s.mapper.pending)
	want := []lipapi.EventKind{lipapi.EventResponseStarted, lipapi.EventMessageStarted, lipapi.EventUsageDelta, lipapi.EventResponseFinished}
	if len(events) != len(want) {
		t.Fatalf("events: %+v", events)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event[%d] = %v, want %v", i, events[i].Kind, kind)
		}
	}
	usage := events[2]
	if usage.InputTokens != 1 || usage.OutputTokens != 2 || usage.TotalTokens != 3 {
		t.Fatalf("usage: %+v", usage)
	}
	if usage.Accounting.Source != lipapi.UsageSourceProviderReported {
		t.Fatalf("usage source=%q", usage.Accounting.Source)
	}
	if usage.Accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("usage authority=%q", usage.Accounting.Authority)
	}
}

func TestHandleData_completedWithoutUsageDoesNotEstimateUsage(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	delta := `{"type":"response.output_text.delta","delta":"world"}`
	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"world"}]}]}}`
	for _, raw := range []string{delta, completed} {
		if err := s.mapper.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}
	events := stream.DrainPending(&s.mapper.pending)
	for _, ev := range events {
		if ev.Kind == lipapi.EventUsageDelta {
			t.Fatalf("raw stream must not estimate usage: %+v", events)
		}
	}
}

func TestUsageEstimatingStream_completedWithoutUsage_generatesEstimatedUsageBeforeFinished(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	base := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "world"},
		{Kind: lipapi.EventResponseFinished},
	})
	s := newUsageEstimatingStream(base, est, call, "gpt-5.3-codex-spark")

	var events []lipapi.Event
	for {
		ev, err := s.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	usageIdx, finishedIdx := -1, -1
	for i, ev := range events {
		switch ev.Kind {
		case lipapi.EventUsageDelta:
			usageIdx = i
		case lipapi.EventResponseFinished:
			finishedIdx = i
		}
	}
	if usageIdx < 0 || finishedIdx < 0 || usageIdx >= finishedIdx {
		t.Fatalf("usage before finished: usageIdx=%d finishedIdx=%d events=%+v", usageIdx, finishedIdx, events)
	}
	usage := events[usageIdx]
	if usage.InputTokens <= 0 || usage.OutputTokens <= 0 || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		t.Fatalf("usage: %+v", usage)
	}
	if usage.Accounting.Source != lipapi.UsageSourceLocalTokenizer || usage.Accounting.Authority != lipapi.UsageAuthorityEstimated {
		t.Fatalf("accounting: %+v", usage.Accounting)
	}
	if usage.Accounting.Plane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("usage plane=%q, want provider_billable", usage.Accounting.Plane)
	}
	if usage.Accounting.Tokenizer.ID != "o200k_base" {
		t.Fatalf("tokenizer id=%q", usage.Accounting.Tokenizer.ID)
	}
}

func TestUsageEstimatingStream_providerUsageIsNotOverridden(t *testing.T) {
	t.Parallel()
	est, err := newUsageEstimator()
	if err != nil {
		t.Fatal(err)
	}
	providerUsage := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  7,
		OutputTokens: 3,
		TotalTokens:  10,
		Accounting: lipapi.UsageAccountingMetadata{
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	base := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "world"},
		providerUsage,
		{Kind: lipapi.EventResponseFinished},
	})
	s := newUsageEstimatingStream(base, est, lipapi.Call{}, "gpt-5.3-codex-spark")

	var usage []lipapi.Event
	for {
		ev, err := s.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == lipapi.EventUsageDelta {
			usage = append(usage, ev)
		}
	}
	if len(usage) != 1 {
		t.Fatalf("usage events = %+v", usage)
	}
	if usage[0].InputTokens != 7 || usage[0].OutputTokens != 3 || usage[0].TotalTokens != 10 {
		t.Fatalf("usage = %+v", usage[0])
	}
	if usage[0].Accounting.Source != lipapi.UsageSourceProviderReported {
		t.Fatalf("source = %q", usage[0].Accounting.Source)
	}
}

func TestHandleData_toolCallStream_mapsToCanonicalToolEvents(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	rawEvents := []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","status":"in_progress","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"\"NYC\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`,
	}
	for _, raw := range rawEvents {
		if err := s.mapper.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}

	var kinds []lipapi.EventKind
	var args strings.Builder
	var toolID string
	for _, ev := range stream.DrainPending(&s.mapper.pending) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == lipapi.EventToolCallStarted {
			toolID = ev.ToolCallID
		}
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
	if toolID != "call_fc_1" {
		t.Fatalf("tool call id = %q, want upstream call_id", toolID)
	}
	if kinds[len(kinds)-1] != lipapi.EventToolCallFinished {
		t.Fatalf("last event: %v", kinds)
	}
}

func TestHandleData_completedOnly_emitsFullText(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}}`
	if err := s.mapper.handleData(completed); err != nil {
		t.Fatal(err)
	}

	var texts []string
	for _, ev := range stream.DrainPending(&s.mapper.pending) {
		if ev.Kind == lipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if len(texts) != 1 || texts[0] != "done" {
		t.Fatalf("texts: %v", texts)
	}
}

func TestHandleData_blocksToolProtocolTextLeak(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	raw := `{"type":"response.output_text.delta","delta":"{\"filePath\":\"C:\\\\repo\\\\file.go\",\"offset\":49,\"limit\":120}to=functions.read"}`
	if err := s.mapper.handleData(raw); err != nil {
		t.Fatal(err)
	}

	events := stream.DrainPending(&s.mapper.pending)
	if len(events) == 0 {
		t.Fatal("expected error event")
	}
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			t.Fatalf("tool protocol leaked as text: %+v", ev)
		}
	}
	last := events[len(events)-1]
	if last.Kind != lipapi.EventError || last.ErrorCode != "tool_protocol_text_leak" {
		t.Fatalf("last event = %+v, want tool protocol leak error", last)
	}
}

func TestHandleData_completed_replaysFunctionCalls(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_fc_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}]}}`
	if err := s.mapper.handleData(completed); err != nil {
		t.Fatal(err)
	}

	var kinds []lipapi.EventKind
	var toolID string
	for _, ev := range stream.DrainPending(&s.mapper.pending) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == lipapi.EventToolCallStarted {
			toolID = ev.ToolCallID
		}
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
	if toolID != "call_fc_1" {
		t.Fatalf("tool call id = %q, want upstream call_id", toolID)
	}
}

func TestHandleData_outputItemDone_emitsCompleteToolCall(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	raw := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_done","call_id":"call_fc_done","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}`
	if err := s.mapper.handleData(raw); err != nil {
		t.Fatal(err)
	}

	var kinds []lipapi.EventKind
	var toolID string
	for _, ev := range stream.DrainPending(&s.mapper.pending) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == lipapi.EventToolCallStarted {
			toolID = ev.ToolCallID
		}
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
	if toolID != "call_fc_done" {
		t.Fatalf("tool call id = %q, want upstream call_id", toolID)
	}
	if len(s.mapper.outputItems) != 1 {
		t.Fatalf("output items = %#v", s.mapper.outputItems)
	}
	item, ok := s.mapper.outputItems[0].(functionCallItem)
	if !ok {
		t.Fatalf("output item = %#v, want functionCallItem", s.mapper.outputItems[0])
	}
	if item.ID != "fc_done" || item.CallID != "call_fc_done" || item.Name != "get_weather" || item.Arguments != `{"city":"NYC"}` {
		t.Fatalf("output item = %#v", item)
	}
}

func TestHandleData_toolCallStream_callIDOnDelta(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	rawEvents := []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","call_id":"call_only","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"call_id":"call_only","output_index":0,"delta":"{\"x\":1}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":2,"call_id":"call_only","output_index":0,"name":"get_weather","arguments":"{\"x\":1}"}`,
	}
	for _, raw := range rawEvents {
		if err := s.mapper.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}

	var toolID string
	for _, ev := range stream.DrainPending(&s.mapper.pending) {
		if ev.Kind == lipapi.EventToolCallStarted {
			toolID = ev.ToolCallID
		}
	}
	if toolID != "call_only" {
		t.Fatalf("tool call id: %q", toolID)
	}
}

func TestHandleData_toolArgsBeforeAddedWaitForToolName(t *testing.T) {
	t.Parallel()
	s := testCodexStream()

	rawEvents := []string{
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_late","output_index":0,"delta":"{\"filePath\":"}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"fc_late","call_id":"call_late","status":"in_progress","name":"read"}}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_late","output_index":0,"name":"read","arguments":"{\"filePath\":\"x\"}"}`,
	}
	for _, raw := range rawEvents {
		if err := s.mapper.handleData(raw); err != nil {
			t.Fatalf("handleData: %v", err)
		}
	}

	events := stream.DrainPending(&s.mapper.pending)
	var toolStarted *lipapi.Event
	var args strings.Builder
	for i := range events {
		ev := events[i]
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			toolStarted = &ev
		case lipapi.EventToolCallArgsDelta:
			args.WriteString(ev.Delta)
		}
	}
	if toolStarted == nil || toolStarted.ToolName != "read" {
		t.Fatalf("tool started = %+v, want name read; events=%+v", toolStarted, events)
	}
	if got := args.String(); got != `{"filePath":` {
		t.Fatalf("args = %q", got)
	}
}
