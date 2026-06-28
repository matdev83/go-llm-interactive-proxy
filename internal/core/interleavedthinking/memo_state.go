// Package interleavedthinking implements core-owned interleaved thinking
// helpers: bounded memo state, the memo store, memo extraction, visible-stream
// sanitization, and candidate-specific call shaping.
//
// Memo state and the memo store are bounded and scoped to an authoritative
// session or A-leg. Raw memo text never exceeds the configured MaxMemoBytes.
package interleavedthinking

import "time"

// MemoState is the persisted thinker memo and its observable metadata.
//
// Memo is the bounded planning content. The remaining fields are operator-
// observable metadata required by Requirements 4.5, 5.3, and 5.4. Raw memo text
// must never exceed the configured memo size limit.
type MemoState struct {
	// Memo is the bounded planning content captured from a thinker turn.
	Memo string
	// SourceSelector is the selector string that selected the thinker branch.
	SourceSelector string
	// Backend identifies the backend instance that produced the memo.
	Backend string
	// Model identifies the model that produced the memo.
	Model string
	// RequestID identifies the thinker request that produced the memo.
	RequestID string
	// CreatedAt is when the memo was captured.
	CreatedAt time.Time
	// InjectedCount is how many executor turns have received this memo.
	InjectedCount int
	// RegularTurnsRemaining is the remaining memo injection budget.
	RegularTurnsRemaining int
	// VisibleToClient records whether the memo content has been surfaced to
	// the client (visible mode). Used to suppress duplicate injection.
	VisibleToClient bool
	// ExtractionSource is "block" or "fallback" describing how Memo was derived.
	ExtractionSource string
	// StreamInterrupted records whether the thinker stream was interrupted
	// before completion while capturing this memo.
	StreamInterrupted bool
}
