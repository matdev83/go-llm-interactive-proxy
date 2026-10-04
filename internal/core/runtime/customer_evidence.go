package runtime

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/plane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// customerEvidenceAccumulator records client-visible canonical content released
// after response hooks and completion-gate resolution, immediately before the
// frontend encoder (design D1/D8). It never observes provider UsageDelta scopes
// or provider money. Released text is the StreamUsage / CountOutput OutputText
// source; reasoning and tool-arg deltas are retained as non-usage events for the
// same counting seam (CountOutput may ignore them — preserve that behavior).
type customerEvidenceAccumulator struct {
	mu        sync.Mutex
	text      strings.Builder
	reasoning strings.Builder
	toolArgs  strings.Builder
	content   []lipapi.Event
	events    int
	settled   atomic.Bool
	// boundary is the byte length of text already attributed to prior
	// attempts of the logical response when the latest continuation was
	// published. Progress detection observes only the text released since
	// this boundary, so identical answers on successive attempts compare
	// equal instead of looking like fresh evidence. Delivery, accounting,
	// and history keep using the cumulative text.
	boundary int
}

func newCustomerEvidenceAccumulator() *customerEvidenceAccumulator {
	return &customerEvidenceAccumulator{}
}

// ObserveReleased records one post-hook/post-gate event about to be released to
// the client. Non-content kinds (usage, keepalive, warning, finish) are ignored.
func (a *customerEvidenceAccumulator) ObserveReleased(ev lipapi.Event) {
	if a == nil {
		return
	}
	var b *strings.Builder
	switch ev.Kind {
	case lipapi.EventTextDelta:
		b = &a.text
	case lipapi.EventReasoningDelta:
		b = &a.reasoning
	case lipapi.EventToolCallArgsDelta:
		b = &a.toolArgs
	default:
		return
	}
	a.mu.Lock()
	b.WriteString(ev.Delta)
	a.content = append(a.content, lipapi.Event{Kind: ev.Kind, Delta: ev.Delta})
	a.events++
	a.mu.Unlock()
}

// Snapshot returns released text and content-event count for settlement.
func (a *customerEvidenceAccumulator) Snapshot() (text, reasoning, toolArgs string, events int) {
	if a == nil {
		return "", "", "", 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.text.String(), a.reasoning.String(), a.toolArgs.String(), a.events
}

// resetContent clears released buffers on B-leg replacement (mirrors visibleText.Reset).
func (a *customerEvidenceAccumulator) resetContent() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.text.Reset()
	a.reasoning.Reset()
	a.toolArgs.Reset()
	a.content = nil
	a.events = 0
	a.boundary = 0
	a.mu.Unlock()
}

// markAttemptBoundary snapshots the current text length as the start of the
// latest continuation's attempt. The newly published attempt has released
// nothing yet, so everything accumulated so far belongs to prior attempts.
// Every continuation re-snapshots. A nil accumulator is a no-op.
func (a *customerEvidenceAccumulator) markAttemptBoundary() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.boundary = a.text.Len()
	a.mu.Unlock()
}

// attemptText reports the text released since the latest attempt boundary, for
// progress detection only. The buffer is append-only between a boundary
// snapshot and the next reset, so the boundary is always a prefix of the
// current text; each appended delta is a complete string, so the cut is always
// on a character boundary. A boundary that is not a usable prefix falls back
// to the cumulative text.
func (a *customerEvidenceAccumulator) attemptText() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	full := a.text.String()
	if a.boundary > 0 && a.boundary <= len(full) {
		return full[a.boundary:]
	}
	return full
}

// contentEvents returns a copy of released content events for StreamUsage.Reconstruct.
func (a *customerEvidenceAccumulator) contentEvents() []lipapi.Event {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.content) == 0 {
		return nil
	}
	out := make([]lipapi.Event, len(a.content))
	copy(out, a.content)
	return out
}

// MarkSettled records that customer authority settlement consumed this snapshot.
// Returns false when settlement already ran (once-only).
func (a *customerEvidenceAccumulator) MarkSettled() bool {
	if a == nil {
		return true
	}
	return a.settled.CompareAndSwap(false, true)
}

func (a *customerEvidenceAccumulator) unmarkSettled() {
	if a == nil {
		return
	}
	a.settled.Store(false)
}

// customerPlaneUsageEvent projects usage evidence safe for customer FE egress /
// settlement: provider_billable scopes and all provider money are removed.
func customerPlaneUsageEvent(ev lipapi.Event) lipapi.Event {
	return plane.CustomerPlaneUsageEvent(ev)
}
