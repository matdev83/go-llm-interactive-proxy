package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestToolEventFromEvent_started(t *testing.T) {
	t.Parallel()
	ev := lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "fn"}
	te, ok := lipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Kind != lipapi.ToolEventStarted || te.ToolCallID != "c1" || te.ToolName != "fn" {
		t.Fatalf("unexpected tool event: %#v", te)
	}
}

func TestToolEventFromEvent_argsDelta(t *testing.T) {
	t.Parallel()
	ev := lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"x":1}`}
	te, ok := lipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Kind != lipapi.ToolEventArgsDelta || te.ArgsDelta != `{"x":1}` {
		t.Fatalf("unexpected tool event: %#v", te)
	}
}

func TestToolEventFromEvent_finished(t *testing.T) {
	t.Parallel()
	ev := lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1"}
	te, ok := lipapi.ToolEventFromEvent(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if te.Kind != lipapi.ToolEventFinished {
		t.Fatalf("unexpected tool event: %#v", te)
	}
}

func TestToolEventFromEvent_rejectsMissingToolCallID(t *testing.T) {
	t.Parallel()
	for _, kind := range []lipapi.EventKind{
		lipapi.EventToolCallStarted,
		lipapi.EventToolCallArgsDelta,
		lipapi.EventToolCallFinished,
	} {
		_, ok := lipapi.ToolEventFromEvent(lipapi.Event{Kind: kind})
		if ok {
			t.Fatalf("expected false for kind %q without tool call id", kind)
		}
	}
}

func TestToolEventFromEvent_nonToolKinds(t *testing.T) {
	t.Parallel()
	_, ok := lipapi.ToolEventFromEvent(lipapi.Event{Kind: lipapi.EventTextDelta})
	if ok {
		t.Fatal("expected false")
	}
}
