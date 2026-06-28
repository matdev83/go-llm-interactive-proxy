// Package interleavedstate holds pure, serializable value types shared by
// routing, continuity, and runtime for interleaved thinking.
//
// The package must stay free of runtime or memo-processing dependencies so it
// can be imported by any core package without introducing cycles. Only the
// standard library is used.
package interleavedstate

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Role labels a cycle entry or routing candidate as thinker, executor, or none.
type Role string

const (
	// RoleNone is the zero value for unannotated entries and candidates.
	RoleNone Role = ""
	// RoleThinker marks a weighted branch that produces a planning memo.
	RoleThinker Role = "thinker"
	// RoleExecutor marks a branch that consumes a memo and produces client output.
	RoleExecutor Role = "executor"
)

// CycleEntry is one position in a thinker-aware weighted cycle. Key identifies
// the branch within its selector; Role records whether it is the thinker branch.
type CycleEntry struct {
	Key  string `json:"key"`
	Role Role   `json:"role"`
}

// CycleState is the selector-scoped cursor for thinker-aware weighted cycles.
// An empty (zero-value) state means no cycle has been established yet.
type CycleState struct {
	SelectorKey string       `json:"selector_key,omitempty"`
	Sequence    []CycleEntry `json:"sequence,omitempty"`
	NextIndex   int          `json:"next_index"`
}

// MemoRef locates a stored memo under an authoritative session or A-leg scope.
// Version is a monotonic counter bumped by the memo store on each mutation so
// callers can detect stale references.
type MemoRef struct {
	Key     string `json:"key"`
	Version int64  `json:"version"`
}

// State bundles the cycle cursor and latest memo reference for one A-leg.
type State struct {
	Cycle   CycleState `json:"cycle"`
	MemoRef *MemoRef   `json:"memo_ref,omitempty"`
}

// IsEmpty reports whether the cycle state has no established sequence.
func (c CycleState) IsEmpty() bool {
	return len(c.Sequence) == 0
}

// Validate checks that a non-empty cycle state has an in-bounds cursor.
// An empty state is valid.
func (c CycleState) Validate() error {
	if c.IsEmpty() {
		if c.NextIndex != 0 {
			return fmt.Errorf("interleavedstate: next_index must be 0 for empty cycle, got %d", c.NextIndex)
		}
		return nil
	}
	if c.NextIndex < 0 || c.NextIndex >= len(c.Sequence) {
		return fmt.Errorf("interleavedstate: next_index %d out of bounds [0,%d)", c.NextIndex, len(c.Sequence))
	}
	return nil
}

// MatchesSelector reports whether the stored cycle is fresh for the given
// selector key. An empty state is considered fresh for any selector (callers
// treat missing state as a new session for cycle purposes).
func (c CycleState) MatchesSelector(selectorKey string) bool {
	if c.IsEmpty() {
		return true
	}
	return c.SelectorKey == selectorKey
}

// Equal reports whether two cycle states have identical selector, sequence, and
// cursor. It is invariant under nil-vs-empty sequence normalization.
func (c CycleState) Equal(other CycleState) bool {
	if c.SelectorKey != other.SelectorKey || c.NextIndex != other.NextIndex {
		return false
	}
	return slices.EqualFunc(c.Sequence, other.Sequence, func(a, b CycleEntry) bool {
		return a.Key == b.Key && a.Role == b.Role
	})
}

// IsEmpty reports whether a memo reference points to no memo.
func (r MemoRef) IsEmpty() bool {
	return r.Key == "" && r.Version == 0
}

// Equal reports whether two memo references identify the same store entry
// revision.
func (r MemoRef) Equal(other MemoRef) bool {
	return r.Key == other.Key && r.Version == other.Version
}

// IsEmpty reports whether the state carries no cycle and no memo reference.
func (s State) IsEmpty() bool {
	return s.Cycle.IsEmpty() && (s.MemoRef == nil || s.MemoRef.IsEmpty())
}

// Validate validates the cycle and memo reference portions of the state.
func (s State) Validate() error {
	if err := s.Cycle.Validate(); err != nil {
		return err
	}
	if s.MemoRef != nil && s.MemoRef.Key == "" && s.MemoRef.Version != 0 {
		return fmt.Errorf("interleavedstate: memo ref with empty key must be zero")
	}
	return nil
}

// Equal reports whether two states have equal cycle and memo reference parts.
func (s State) Equal(other State) bool {
	if !s.Cycle.Equal(other.Cycle) {
		return false
	}
	if (s.MemoRef == nil) != (other.MemoRef == nil) {
		return false
	}
	if s.MemoRef == nil {
		return true
	}
	return s.MemoRef.Equal(*other.MemoRef)
}

// MarshalStateText serializes state for compact durable storage. Empty state is
// encoded as an empty string so old rows and non-thinker routes stay cheap.
func MarshalStateText(state State) (string, error) {
	if state.IsEmpty() {
		return "", nil
	}
	b, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalStateText decodes state produced by [MarshalStateText]. An empty
// string is the zero state for backward-compatible durable rows.
func UnmarshalStateText(encoded string) (State, error) {
	if encoded == "" {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return State{}, err
	}
	return state, nil
}

// MarshalJSON serializes CycleState with nil sequence normalized to empty so
// round-trip equality holds for zero-value inputs.
func (c CycleState) MarshalJSON() ([]byte, error) {
	type alias CycleState
	out := alias(c)
	if out.Sequence == nil {
		out.Sequence = []CycleEntry{}
	}
	return json.Marshal(out)
}
