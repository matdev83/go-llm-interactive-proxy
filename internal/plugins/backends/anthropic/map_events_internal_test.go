package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestHandleEvent_thinkingDeltaFromJSON(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason-chunk"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	_ = s.handleEvent(u)
	var got []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventReasoningDelta {
			got = append(got, ev.Delta)
		}
	}
	if len(got) != 1 || got[0] != "reason-chunk" {
		t.Fatalf("reasoning deltas: %v", got)
	}
}

func TestHandleEvent_assistantImageURLContentBlockStart(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":0,"content_block":{"type":"image","source":{"type":"url","url":"https://cdn.example.com/out.png"}}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	_ = s.handleEvent(u)
	var refs []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventAssistantImageRef {
			refs = append(refs, ev.AssistantRef)
		}
	}
	if len(refs) != 1 || refs[0] != "https://cdn.example.com/out.png" {
		t.Fatalf("assistant image refs: %v", refs)
	}
}

func TestHandleEvent_assistantDocumentURLContentBlockStart(t *testing.T) {
	t.Parallel()
	raw := `{"type":"content_block_start","index":1,"content_block":{"type":"document","source":{"type":"url","url":"https://files.example.com/a.pdf","media_type":"application/pdf"},"title":"A"}}`
	var u anthropic.MessageStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &msgStream{}
	_ = s.handleEvent(u)
	var got []lipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == lipapi.EventAssistantFileRef {
			got = append(got, ev)
		}
	}
	if len(got) != 1 || got[0].AssistantRef != "https://files.example.com/a.pdf" || got[0].AssistantMIME != "application/pdf" || got[0].AssistantName != "A" {
		t.Fatalf("assistant file event: %+v", got)
	}
}

//nolint:paralleltest // wire steps share msgStream; inner t.Run is for failure attribution only
func TestHandleEvent_toolUseStreamFromJSON(t *testing.T) {
	t.Parallel()
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"NYC\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	s := &msgStream{}
	for i, raw := range events {
		t.Run(fmt.Sprintf("wire_step_%d", i), func(t *testing.T) {
			var u anthropic.MessageStreamEventUnion
			if err := json.Unmarshal([]byte(raw), &u); err != nil {
				t.Fatalf("unmarshal %q: %v", raw, err)
			}
			_ = s.handleEvent(u)
		})
	}
	var names []string
	var args strings.Builder
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			names = append(names, ev.ToolName)
		case lipapi.EventToolCallArgsDelta:
			args.WriteString(ev.Delta)
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID != "toolu_01" {
				t.Fatalf("finish id: %q", ev.ToolCallID)
			}
		}
	}
	if len(names) != 1 || names[0] != "get_weather" {
		t.Fatalf("tool names: %v", names)
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("args concatenated: %q", got)
	}
}

type errDecoderAnthropic struct{ err error }

func (d *errDecoderAnthropic) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)}
}

func (d *errDecoderAnthropic) Next() bool { return false }

func (d *errDecoderAnthropic) Close() error { return nil }

func (d *errDecoderAnthropic) Err() error { return d.err }

func TestMsgStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	sdk := ssestream.NewStream[anthropic.MessageStreamEventUnion](&errDecoderAnthropic{err: root}, nil)
	es := newMessageStream(sdk, 0)
	s, ok := es.(*msgStream)
	if !ok {
		t.Fatalf("newMessageStream returned %T", es)
	}
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "anthropic: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}

func TestMsgStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := &msgStream{}
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect lipapi.ErrNilContext
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}
