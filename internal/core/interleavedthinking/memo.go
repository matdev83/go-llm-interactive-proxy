package interleavedthinking

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Memo wrapper tags delimit thinker planning output. A complete block is
// captured as the memo body; absent or incomplete blocks fall back to a bounded
// aggregate of the thinker output.
const (
	MemoOpenTag  = "<proxy_thinker_memo>"
	MemoCloseTag = "</proxy_thinker_memo>"

	ExtractionSourceBlock    = "block"
	ExtractionSourceFallback = "fallback"
)

type extractorPhase int

const (
	phaseSearching extractorPhase = iota
	phaseCapturing
	phaseDone
)

// Recorder observes canonical thinker stream events, extracts a bounded
// <proxy_thinker_memo>...</proxy_thinker_memo> block when a complete one is
// present, and produces a bounded MemoState at stream completion.
//
// Observe also sanitizes the visible stream: memo wrapper tags are stripped
// (including across delta boundaries) and thinker content is emitted as
// canonical EventReasoningDelta so wrapper tags never surface as ordinary
// assistant text. Memo content and the fallback aggregate are both bounded
// by MaxMemoBytes; a zero MaxMemoBytes disables bounding, matching the memo
// store convention.
//
// Identity metadata fields are set by the caller before Finish and copied into
// the resulting MemoState. Injection metadata (InjectedCount, VisibleToClient)
// is left zero at capture time.
type Recorder struct {
	MaxMemoBytes int

	// Identity metadata copied into the resulting MemoState.
	SourceSelector        string
	Backend               string
	Model                 string
	RequestID             string
	RegularTurnsRemaining int

	phase          extractorPhase
	openTail       string
	closeTail      string
	blockBuf       strings.Builder
	blockComplete  bool
	blockTruncated bool

	fallbackBuf       strings.Builder
	fallbackTruncated bool

	sanInTag   bool
	sanPending strings.Builder
}

// Observe processes one canonical event: it feeds text/reasoning deltas to
// memo extraction and returns sanitized visible events. Returned content
// deltas are always EventReasoningDelta with memo wrapper tags stripped.
// Non-content events pass through unchanged; call FlushVisibleSanitizer at
// stream end to emit any buffered partial-tag visible content.
func (r *Recorder) Observe(ev lipapi.Event) []lipapi.Event {
	switch ev.Kind {
	case lipapi.EventTextDelta, lipapi.EventReasoningDelta:
		r.ingest(ev.Delta)
		if sanitized := r.sanitizeDelta(ev.Delta); sanitized != "" {
			return []lipapi.Event{{Kind: lipapi.EventReasoningDelta, Delta: sanitized}}
		}
		return nil
	default:
		return []lipapi.Event{ev}
	}
}

func (r *Recorder) ingest(delta string) {
	if delta == "" {
		return
	}
	r.appendFallback(delta)
	switch r.phase {
	case phaseDone:
		return
	case phaseSearching:
		combined := r.openTail + delta
		if _, after, ok := strings.Cut(combined, MemoOpenTag); ok {
			r.openTail = ""
			r.phase = phaseCapturing
			r.capture(after)
		} else {
			r.openTail = keepTail(combined, len(MemoOpenTag)-1)
		}
	case phaseCapturing:
		r.capture(r.closeTail + delta)
	}
}

func (r *Recorder) capture(chunk string) {
	if before, _, ok := strings.Cut(chunk, MemoCloseTag); ok {
		r.writeBlock(before)
		r.blockComplete = true
		r.phase = phaseDone
		r.closeTail = ""
		return
	}
	keep := len(MemoCloseTag) - 1
	if len(chunk) <= keep {
		r.closeTail = chunk
		return
	}
	r.writeBlock(chunk[:len(chunk)-keep])
	r.closeTail = chunk[len(chunk)-keep:]
}

func (r *Recorder) writeBlock(s string) {
	r.writeBounded(&r.blockBuf, s, &r.blockTruncated)
}

func (r *Recorder) appendFallback(s string) {
	r.writeBounded(&r.fallbackBuf, s, &r.fallbackTruncated)
}

func (r *Recorder) writeBounded(b *strings.Builder, s string, truncated *bool) {
	if *truncated {
		return
	}
	limit := r.MaxMemoBytes
	if limit <= 0 {
		b.WriteString(s)
		return
	}
	remaining := limit - b.Len()
	if remaining <= 0 {
		*truncated = true
		return
	}
	if len(s) <= remaining {
		b.WriteString(s)
		return
	}
	b.WriteString(s[:remaining])
	*truncated = true
}

// FlushVisibleSanitizer returns any buffered partial-tag visible content and
// should be called once at stream end before Finish when surfacing thinker output.
func (r *Recorder) FlushVisibleSanitizer() []lipapi.Event {
	if flushed := r.flushSanitizer(); flushed != "" {
		return []lipapi.Event{{Kind: lipapi.EventReasoningDelta, Delta: flushed}}
	}
	return nil
}

// Finish returns the captured MemoState. A complete block yields block content;
// otherwise a bounded aggregate of the thinker output is used as fallback.
// interrupted marks the stored memo as captured from an interrupted stream.
func (r *Recorder) Finish(interrupted bool) MemoState {
	state := MemoState{
		SourceSelector:        r.SourceSelector,
		Backend:               r.Backend,
		Model:                 r.Model,
		RequestID:             r.RequestID,
		CreatedAt:             time.Now(),
		RegularTurnsRemaining: r.RegularTurnsRemaining,
		StreamInterrupted:     interrupted,
	}
	if r.blockComplete {
		state.Memo = r.blockBuf.String()
		state.ExtractionSource = ExtractionSourceBlock
		return state
	}
	state.Memo = r.fallbackBuf.String()
	state.ExtractionSource = ExtractionSourceFallback
	return state
}

// keepTail returns the suffix of s of length at most keep, used to detect a
// delimiter tag split across deltas. If s is shorter than keep, s is returned
// unchanged so the next delta can complete the candidate.
func keepTail(s string, keep int) string {
	if keep <= 0 || len(s) <= keep {
		return s
	}
	return s[len(s)-keep:]
}
