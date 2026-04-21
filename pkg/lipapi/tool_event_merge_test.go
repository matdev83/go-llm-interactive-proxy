package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMergeToolEventInto_rewriteArgsDelta(t *testing.T) {
	t.Parallel()
	orig := lipapi.Event{
		Kind:       lipapi.EventToolCallArgsDelta,
		ToolCallID: "call_1",
		ToolName:   "weather",
		Delta:      `{"a":1}`,
	}
	te := lipapi.ToolEvent{
		Kind:       lipapi.ToolEventArgsDelta,
		ToolCallID: "call_1",
		ToolName:   "weather_v2",
		ArgsDelta:  `{"b":2}`,
	}
	got := lipapi.MergeToolEventInto(orig, te)
	if got.Delta != `{"b":2}` || got.ToolName != "weather_v2" {
		t.Fatalf("got %+v", got)
	}
}

func TestMergeToolEventInto_startedRewritesID(t *testing.T) {
	t.Parallel()
	orig := lipapi.Event{
		Kind:       lipapi.EventToolCallStarted,
		ToolCallID: "old",
		ToolName:   "fn",
	}
	te := lipapi.ToolEvent{
		Kind:       lipapi.ToolEventStarted,
		ToolCallID: "new",
		ToolName:   "fn2",
	}
	got := lipapi.MergeToolEventInto(orig, te)
	if got.ToolCallID != "new" || got.ToolName != "fn2" {
		t.Fatalf("got %+v", got)
	}
}
