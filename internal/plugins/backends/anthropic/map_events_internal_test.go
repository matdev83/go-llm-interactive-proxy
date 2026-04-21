package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

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
	s.handleEvent(u)
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

func TestHandleEvent_toolUseStreamFromJSON(t *testing.T) {
	t.Parallel()
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"NYC\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	s := &msgStream{}
	for _, raw := range events {
		var u anthropic.MessageStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatalf("unmarshal %q: %v", raw, err)
		}
		s.handleEvent(u)
	}
	var names []string
	var args string
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			names = append(names, ev.ToolName)
		case lipapi.EventToolCallArgsDelta:
			args += ev.Delta
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID != "toolu_01" {
				t.Fatalf("finish id: %q", ev.ToolCallID)
			}
		}
	}
	if len(names) != 1 || names[0] != "get_weather" {
		t.Fatalf("tool names: %v", names)
	}
	if args != `{"city":"NYC"}` {
		t.Fatalf("args concatenated: %q", args)
	}
}
