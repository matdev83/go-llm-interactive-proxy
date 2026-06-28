package interleavedthinking

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// collectSanitized feeds events through r.Observe and returns the visible
// events it produces, failing the test if any wrapper tag substring leaks.
func collectSanitized(t *testing.T, r *Recorder, events []lipapi.Event) []lipapi.Event {
	t.Helper()
	var out []lipapi.Event
	for _, ev := range events {
		out = append(out, r.Observe(ev)...)
	}
	for i, ev := range out {
		if strings.Contains(ev.Delta, memoOpenTag) || strings.Contains(ev.Delta, memoCloseTag) {
			t.Fatalf("wrapper tag leaked into visible event %d: %+v", i, ev)
		}
	}
	return out
}

// onlyReasoningDeltas fails if any event in out is not an EventReasoningDelta.
func onlyReasoningDeltas(t *testing.T, out []lipapi.Event) {
	t.Helper()
	for i, ev := range out {
		if ev.Kind != lipapi.EventReasoningDelta {
			t.Fatalf("visible event %d must be reasoning delta, got %q: %+v", i, ev.Kind, ev)
		}
	}
}

func concatDeltas(out []lipapi.Event) string {
	var b strings.Builder
	for _, ev := range out {
		b.WriteString(ev.Delta)
	}
	return b.String()
}

func TestSanitize_TextDeltaEmittedAsReasoning(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("thinker plan without tags"),
	})
	onlyReasoningDeltas(t, out)
	if concatDeltas(out) != "thinker plan without tags" {
		t.Fatalf("expected content preserved, got %q", concatDeltas(out))
	}
}

func TestSanitize_ReasoningDeltaStaysReasoning(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		reasoningDelta("reasoned plan without tags"),
	})
	onlyReasoningDeltas(t, out)
	if concatDeltas(out) != "reasoned plan without tags" {
		t.Fatalf("expected content preserved, got %q", concatDeltas(out))
	}
}

func TestSanitize_StripsWrapperTagsFromText(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("intro " + memoOpenTag + "the plan" + memoCloseTag + " outro"),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "intro the plan outro" {
		t.Fatalf("expected tags stripped, got %q", got)
	}
}

func TestSanitize_StripsWrapperTagsFromReasoning(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		reasoningDelta(memoOpenTag + "reasoned plan" + memoCloseTag),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "reasoned plan" {
		t.Fatalf("expected tags stripped, got %q", got)
	}
}

func TestSanitize_TagsSplitAcrossTextDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("intro " + memoOpenTag + "the "),
		textDelta("plan"),
		textDelta(memoCloseTag + " outro"),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "intro the plan outro" {
		t.Fatalf("expected split tags stripped, got %q", got)
	}
}

func TestSanitize_TagsSplitAcrossReasoningDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		reasoningDelta(memoOpenTag + "reasoned "),
		reasoningDelta("plan" + memoCloseTag + " tail"),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "reasoned plan tail" {
		t.Fatalf("expected split tags stripped, got %q", got)
	}
}

func TestSanitize_SplitOpenTagAcrossDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("intro <proxy_thinker_me"),
		textDelta("mo>the plan" + memoCloseTag + " outro"),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "intro the plan outro" {
		t.Fatalf("expected split open tag stripped, got %q", got)
	}
}

func TestSanitize_SplitCloseTagAcrossDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "the plan</proxy_thinker_me"),
		textDelta("mo> outro"),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "the plan outro" {
		t.Fatalf("expected split close tag stripped, got %q", got)
	}
}

func TestSanitize_NonContentEventsPassThrough(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := r.Observe(lipapi.Event{Kind: lipapi.EventResponseStarted})
	if len(out) != 1 || out[0].Kind != lipapi.EventResponseStarted {
		t.Fatalf("non-content event must pass through, got %+v", out)
	}
}

func TestSanitize_FlushesPartialTagOnTerminalEvent(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("plan <proxy_thinker_me"),
		{Kind: lipapi.EventResponseFinished},
	})
	onlyReasoningDeltas(t, out[:len(out)-1])
	if out[len(out)-1].Kind != lipapi.EventResponseFinished {
		t.Fatalf("terminal event must pass through, got %+v", out[len(out)-1])
	}
	if got := concatDeltas(out[:len(out)-1]); got != "plan <proxy_thinker_me" {
		t.Fatalf("partial tag must flush as content, got %q", got)
	}
}

func TestSanitize_LookalikeTagFlushedAsContent(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("<proxy_thinker_memoX not a tag>"),
	})
	onlyReasoningDeltas(t, out)
	if got := concatDeltas(out); got != "<proxy_thinker_memoX not a tag>" {
		t.Fatalf("lookalike must flush as content, got %q", got)
	}
}

func TestSanitize_PreservesMemoExtraction(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := collectSanitized(t, r, []lipapi.Event{
		textDelta("intro " + memoOpenTag + "the plan" + memoCloseTag + " outro"),
	})
	onlyReasoningDeltas(t, out)
	state := r.Finish(false)
	if state.Memo != "the plan" {
		t.Fatalf("expected block memo %q, got %q", "the plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
	if got := concatDeltas(out); got != "intro the plan outro" {
		t.Fatalf("expected sanitized visible output, got %q", got)
	}
}

func TestSanitize_EmptyDeltaEmitsNothing(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	out := r.Observe(textDelta(""))
	if len(out) != 0 {
		t.Fatalf("empty delta must emit nothing, got %+v", out)
	}
}
