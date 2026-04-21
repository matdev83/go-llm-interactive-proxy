package acp

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestParseNDJSONLine_planEmitsReasoning(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventReasoningDelta {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_planDisabledYieldsWarning(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"plan","entries":[]}}}`
	mapper := mergeMapperOptions(Config{SessionUpdate: SessionUpdateMapperOptions{DisablePlanReasoning: true}})
	evs, err := parseNDJSONLine(context.Background(), mapper, line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventWarning {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_chunkYieldsDelta(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventTextDelta || evs[0].Delta != "hi" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_textDelta(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"textDelta":"x"}}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Delta != "x" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_terminal(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":10,"result":{"stopReason":"end_turn"}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != lipapi.EventResponseFinished {
		t.Fatalf("got %#v", evs)
	}
}
