package interleavedthinking

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	memoOpenTag  = "<proxy_thinker_memo>"
	memoCloseTag = "</proxy_thinker_memo>"
)

func newRecorder(maxBytes int) *Recorder {
	return &Recorder{
		MaxMemoBytes:          maxBytes,
		SourceSelector:        "openai-responses:gpt-4o[thinker]",
		Backend:               "openai-responses",
		Model:                 "gpt-4o",
		RequestID:             "req-1",
		RegularTurnsRemaining: 2,
	}
}

func textDelta(s string) lipapi.Event {
	return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: s}
}

func reasoningDelta(s string) lipapi.Event {
	return lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: s}
}

func observeAll(t *testing.T, r *Recorder, events []lipapi.Event) {
	t.Helper()
	for _, ev := range events {
		r.Observe(ev)
	}
}

func TestRecorder_BlockFromTextDeltas_OneDelta(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta("intro " + memoOpenTag + "the plan" + memoCloseTag + " outro"),
	})
	state := r.Finish(false)
	if state.Memo != "the plan" {
		t.Fatalf("expected block memo %q, got %q", "the plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
	if state.StreamInterrupted {
		t.Fatal("expected StreamInterrupted=false")
	}
}

func TestRecorder_BlockFromTextDeltas_SplitAcrossDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta("intro " + memoOpenTag + "the "),
		textDelta("plan"),
		textDelta(memoCloseTag + " outro"),
	})
	state := r.Finish(false)
	if state.Memo != "the plan" {
		t.Fatalf("expected block memo %q, got %q", "the plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_BlockFromTextDeltas_SplitOpenTag(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta("intro <proxy_thinker_me"),
		textDelta("mo>the plan" + memoCloseTag + " outro"),
	})
	state := r.Finish(false)
	if state.Memo != "the plan" {
		t.Fatalf("expected block memo %q, got %q", "the plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_BlockFromTextDeltas_SplitCloseTag(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "the plan</proxy_thinker_me"),
		textDelta("mo> outro"),
	})
	state := r.Finish(false)
	if state.Memo != "the plan" {
		t.Fatalf("expected block memo %q, got %q", "the plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_BlockFromReasoningDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		reasoningDelta("thinking... "),
		reasoningDelta(memoOpenTag + "reasoned plan" + memoCloseTag),
	})
	state := r.Finish(false)
	if state.Memo != "reasoned plan" {
		t.Fatalf("expected reasoning block memo %q, got %q", "reasoned plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_BlockFromReasoningDeltas_Split(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		reasoningDelta(memoOpenTag + "reasoned "),
		reasoningDelta("plan" + memoCloseTag + " tail"),
	})
	state := r.Finish(false)
	if state.Memo != "reasoned plan" {
		t.Fatalf("expected reasoning block memo %q, got %q", "reasoned plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_AbsentWrappersFallback(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta("just a normal "),
		reasoningDelta("thinker response without tags"),
	})
	state := r.Finish(false)
	if state.Memo != "just a normal thinker response without tags" {
		t.Fatalf("expected fallback aggregate %q, got %q", "just a normal thinker response without tags", state.Memo)
	}
	if state.ExtractionSource != "fallback" {
		t.Fatalf("expected extraction source fallback, got %q", state.ExtractionSource)
	}
}

func TestRecorder_IncompleteWrapperFallback(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta("intro " + memoOpenTag + "partial plan with no close"),
	})
	state := r.Finish(false)
	if state.ExtractionSource != "fallback" {
		t.Fatalf("incomplete block must use fallback, got %q", state.ExtractionSource)
	}
	if state.Memo == "partial plan with no close" {
		t.Fatalf("fallback must be the aggregate output, not the partial block body")
	}
	if state.Memo == "" {
		t.Fatal("fallback memo must not be empty")
	}
}

func TestRecorder_IncompleteWrapperFallback_OnlyOpenTag(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag),
	})
	state := r.Finish(false)
	if state.ExtractionSource != "fallback" {
		t.Fatalf("lone open tag must use fallback, got %q", state.ExtractionSource)
	}
}

func TestRecorder_ByteLimit_TruncatesBlock(t *testing.T) {
	t.Parallel()
	r := newRecorder(8)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "plan12345" + memoCloseTag),
	})
	state := r.Finish(false)
	if state.ExtractionSource != "block" {
		t.Fatalf("complete block with truncated body is still a block, got %q", state.ExtractionSource)
	}
	if len(state.Memo) > 8 {
		t.Fatalf("bounded block memo must not exceed limit: got %d bytes %q", len(state.Memo), state.Memo)
	}
	if state.Memo != "plan12345"[:8] {
		t.Fatalf("expected truncated block %q, got %q", "plan12345"[:8], state.Memo)
	}
}

func TestRecorder_ByteLimit_TruncatesFallback(t *testing.T) {
	t.Parallel()
	r := newRecorder(8)
	observeAll(t, r, []lipapi.Event{
		textDelta("abcdefghij"),
	})
	state := r.Finish(false)
	if state.ExtractionSource != "fallback" {
		t.Fatalf("expected fallback, got %q", state.ExtractionSource)
	}
	if len(state.Memo) > 8 {
		t.Fatalf("bounded fallback memo must not exceed limit: got %d bytes %q", len(state.Memo), state.Memo)
	}
	if state.Memo != "abcdefghij"[:8] {
		t.Fatalf("expected truncated fallback %q, got %q", "abcdefghij"[:8], state.Memo)
	}
}

func TestRecorder_ByteLimit_ZeroDisables(t *testing.T) {
	t.Parallel()
	r := newRecorder(0)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "an unbounded plan body that is longer than any small limit" + memoCloseTag),
	})
	state := r.Finish(false)
	if state.ExtractionSource != "block" {
		t.Fatalf("expected block, got %q", state.ExtractionSource)
	}
	if state.Memo != "an unbounded plan body that is longer than any small limit" {
		t.Fatalf("zero limit must not truncate, got %q", state.Memo)
	}
}

func TestRecorder_InterruptedStreamMetadata(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta("partial output before "),
		textDelta(memoOpenTag + "partial block with no close"),
	})
	state := r.Finish(true)
	if !state.StreamInterrupted {
		t.Fatal("Finish(true) must set StreamInterrupted")
	}
	if state.ExtractionSource != "fallback" {
		t.Fatalf("interrupted incomplete block must use fallback, got %q", state.ExtractionSource)
	}
}

func TestRecorder_InterruptedStreamMetadata_CompleteBlock(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "complete plan" + memoCloseTag),
	})
	state := r.Finish(true)
	if !state.StreamInterrupted {
		t.Fatal("Finish(true) must set StreamInterrupted even with complete block")
	}
	if state.Memo != "complete plan" {
		t.Fatalf("expected block memo %q, got %q", "complete plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_PreservesMetadata(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "plan" + memoCloseTag),
	})
	state := r.Finish(false)
	if state.SourceSelector != "openai-responses:gpt-4o[thinker]" {
		t.Fatalf("expected SourceSelector preserved, got %q", state.SourceSelector)
	}
	if state.Backend != "openai-responses" || state.Model != "gpt-4o" || state.RequestID != "req-1" {
		t.Fatalf("expected identity metadata preserved, got %+v", state)
	}
	if state.RegularTurnsRemaining != 2 {
		t.Fatalf("expected RegularTurnsRemaining preserved, got %d", state.RegularTurnsRemaining)
	}
	if state.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set at Finish")
	}
	if state.InjectedCount != 0 || state.VisibleToClient {
		t.Fatalf("capture must not set injection fields, got injected=%d visible=%v", state.InjectedCount, state.VisibleToClient)
	}
}

func TestRecorder_IgnoresNonContentEvents(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		textDelta(memoOpenTag + "plan" + memoCloseTag),
		{Kind: lipapi.EventUsageDelta, InputTokens: 10},
		{Kind: lipapi.EventResponseFinished},
	})
	state := r.Finish(false)
	if state.Memo != "plan" {
		t.Fatalf("expected block memo %q, got %q", "plan", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}

func TestRecorder_OnlyFirstBlockCaptured(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []lipapi.Event{
		textDelta(memoOpenTag + "first" + memoCloseTag + " middle " + memoOpenTag + "second" + memoCloseTag),
	})
	state := r.Finish(false)
	if state.Memo != "first" {
		t.Fatalf("expected first block %q, got %q", "first", state.Memo)
	}
	if state.ExtractionSource != "block" {
		t.Fatalf("expected extraction source block, got %q", state.ExtractionSource)
	}
}
