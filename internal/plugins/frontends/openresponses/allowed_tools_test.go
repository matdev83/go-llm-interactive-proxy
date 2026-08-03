package openresponses

import (
	"context"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestAllowedToolsStream_suppressesForbiddenToolCallLifecycle(t *testing.T) {
	call := &lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "forbidden_fn"}, {Name: "allowed_fn"}},
		ToolChoice: lipapi.ToolChoice{
			Mode:         lipapi.ToolChoiceAuto,
			AllowedTools: []string{"allowed_fn"},
		},
	}
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_bad", ToolName: "forbidden_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_bad", Delta: `{"q":"leak"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_bad"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_good", ToolName: "allowed_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_good", Delta: `{"q":"ok"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_good"},
		{Kind: lipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, lipapi.NewFixedEventStream(events))

	var got []lipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		got = append(got, ev)
	}

	for _, ev := range got {
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			if ev.ToolName == "forbidden_fn" {
				t.Fatalf("forbidden tool call started leaked: %+v", ev)
			}
		case lipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_bad" {
				t.Fatalf("forbidden tool call args leaked: %+v", ev)
			}
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_bad" {
				t.Fatalf("forbidden tool call finished leaked: %+v", ev)
			}
		}
	}

	sawAllowed := false
	for _, ev := range got {
		if ev.Kind == lipapi.EventToolCallStarted && ev.ToolName == "allowed_fn" {
			sawAllowed = true
		}
	}
	if !sawAllowed {
		t.Fatalf("allowed tool call was dropped: %+v", got)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 events after suppression (start, msg, allowed started/delta/finished, finished), got %d: %+v", len(got), got)
	}
}

func TestAllowedToolsStream_clearsSuppressedCallOnFinish(t *testing.T) {
	call := &lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "a"}, {Name: "b"}},
		ToolChoice: lipapi.ToolChoice{
			Mode:         lipapi.ToolChoiceAuto,
			AllowedTools: []string{"a"},
		},
	}
	// Two parallel forbidden calls plus an allowed call; the suppression state
	// must be keyed per call ID so one finished forbidden call does not leak
	// into the other.
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "bad1", ToolName: "b"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "bad1", Delta: "1"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "bad1"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "bad2", ToolName: "b"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "bad2", Delta: "2"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "bad2"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "good", ToolName: "a"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "good", Delta: "3"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "good"},
		{Kind: lipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, lipapi.NewFixedEventStream(events))

	seen := map[string]bool{}
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		if ev.Kind == lipapi.EventToolCallStarted {
			seen[ev.ToolCallID] = true
		}
		if ev.Kind == lipapi.EventToolCallArgsDelta && ev.ToolCallID != "good" {
			t.Fatalf("forbidden args leaked: %+v", ev)
		}
	}
	if seen["bad1"] || seen["bad2"] {
		t.Fatalf("forbidden starts leaked: %v", seen)
	}
	if !seen["good"] {
		t.Fatalf("allowed start missing: %v", seen)
	}
}

func TestAllowedToolsStream_withoutSubsetPassesThrough(t *testing.T) {
	call := &lipapi.Call{Tools: []lipapi.ToolDef{{Name: "any_fn"}}, ToolChoice: lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}}
	raw := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "c", ToolName: "any_fn"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "c"},
	})
	stream := newAllowedToolsStream(call, raw)
	if stream != raw {
		t.Fatalf("expected identity passthrough when no subset is configured")
	}
}

func TestAllowedToolsStream_modeNoneSuppressesAllToolCalls(t *testing.T) {
	// allowed_tools mode none is legal (the subset is vacuous) but the model
	// must never call a tool: even subset members must be fully suppressed.
	call := &lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "forbidden_fn"}, {Name: "allowed_fn"}},
		ToolChoice: lipapi.ToolChoice{
			Mode:         lipapi.ToolChoiceNone,
			AllowedTools: []string{"allowed_fn"},
		},
	}
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_bad", ToolName: "forbidden_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_bad", Delta: `{"q":"leak"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_bad"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_allowed", ToolName: "allowed_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_allowed", Delta: `{"q":"also-leak"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_allowed"},
		{Kind: lipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, lipapi.NewFixedEventStream(events))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		switch ev.Kind {
		case lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
			t.Fatalf("mode none leaked tool call lifecycle event: %+v", ev)
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected only response/message/finish envelope (3 events), got %d: %+v", len(got), got)
	}
}

func TestAllowedToolsStream_modeNoneWithEmptySubsetSuppressesAllToolCalls(t *testing.T) {
	call := &lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "any_fn"}},
		ToolChoice: lipapi.ToolChoice{
			Mode: lipapi.ToolChoiceNone,
		},
	}
	stream := newAllowedToolsStream(call, lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_any", ToolName: "any_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_any", Delta: "{}"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_any"},
		{Kind: lipapi.EventResponseFinished},
	}))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		switch ev.Kind {
		case lipapi.EventToolCallStarted, lipapi.EventToolCallArgsDelta, lipapi.EventToolCallFinished:
			t.Fatalf("empty-subset mode none leaked tool lifecycle event: %+v", ev)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected response start/finish only, got %d: %+v", len(got), got)
	}
}

func TestAllowedToolsStream_emptyIDEventsFailClosedWhenInterleaved(t *testing.T) {
	// When allowed and suppressed calls interleave, an empty-ID args/finish
	// event could belong to a suppressed call. It must be dropped (fail
	// closed), and bookkeeping must stay consistent so later empty-ID events
	// are handled once no suppressed call is open.
	call := &lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "a"}, {Name: "b"}},
		ToolChoice: lipapi.ToolChoice{
			Mode:         lipapi.ToolChoiceAuto,
			AllowedTools: []string{"a"},
		},
	}
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "bad", ToolName: "b"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "good", ToolName: "a"},
		{Kind: lipapi.EventToolCallArgsDelta, Delta: "ambig"},
		{Kind: lipapi.EventToolCallFinished},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "good"},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "bad"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "good2", ToolName: "a"},
		{Kind: lipapi.EventToolCallArgsDelta, Delta: "ok"},
		{Kind: lipapi.EventToolCallFinished},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "good2"},
		{Kind: lipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, lipapi.NewFixedEventStream(events))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		if ev.Kind == lipapi.EventToolCallArgsDelta && ev.Delta == "ambig" {
			t.Fatalf("empty-ID args delta leaked while a suppressed call was open: %+v", got)
		}
	}
	sawOK := false
	sawGood := false
	for _, ev := range got {
		switch ev.Kind {
		case lipapi.EventToolCallArgsDelta:
			if ev.Delta == "ok" {
				sawOK = true
			}
		case lipapi.EventToolCallStarted:
			if ev.ToolCallID == "good" || ev.ToolCallID == "good2" {
				sawGood = true
			}
		}
	}
	if !sawOK {
		t.Fatalf("empty-ID args delta after suppressed call closed should pass: %+v", got)
	}
	if !sawGood {
		t.Fatalf("allowed calls were dropped: %+v", got)
	}
}

func TestAllowedToolsStream_modeRequiredForbiddenCallsStaySuppressed(t *testing.T) {
	// allowed_tools mode required (canonical any) must still keep forbidden
	// calls fully suppressed; no terminal enforcement is added beyond what the
	// filter already guarantees.
	call := &lipapi.Call{
		Tools: []lipapi.ToolDef{{Name: "allowed_fn"}, {Name: "forbidden_fn"}},
		ToolChoice: lipapi.ToolChoice{
			Mode:         lipapi.ToolChoiceAny,
			AllowedTools: []string{"allowed_fn"},
		},
	}
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_forbidden", ToolName: "forbidden_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_forbidden", Delta: `{"q":"leak"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_forbidden"},
		{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_allowed", ToolName: "allowed_fn"},
		{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_allowed", Delta: `{"q":"ok"}`},
		{Kind: lipapi.EventToolCallFinished, ToolCallID: "call_allowed"},
		{Kind: lipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, lipapi.NewFixedEventStream(events))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		switch ev.Kind {
		case lipapi.EventToolCallStarted:
			if ev.ToolName == "forbidden_fn" {
				t.Fatalf("required-mode forbidden tool call leaked: %+v", ev)
			}
		case lipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_forbidden" {
				t.Fatalf("required-mode forbidden args leaked: %+v", ev)
			}
		case lipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_forbidden" {
				t.Fatalf("required-mode forbidden finish leaked: %+v", ev)
			}
		}
	}
	sawAllowed := false
	for _, ev := range got {
		if ev.Kind == lipapi.EventToolCallStarted && ev.ToolName == "allowed_fn" {
			sawAllowed = true
		}
	}
	if !sawAllowed {
		t.Fatalf("required-mode allowed call was dropped: %+v", got)
	}
}

func drainAllowedToolsStream(t *testing.T, stream lipapi.EventStream) []lipapi.Event {
	t.Helper()
	var got []lipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		got = append(got, ev)
	}
}
