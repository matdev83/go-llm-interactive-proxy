package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

// interleavedCyclePosition returns the 1-based position of the selected cycle
// entry and the cycle length for diagnostics. It derives the consumed slot
// from the persisted cycle before planning (pre) and the advanced state the
// plan returned (post): the selected slot is the one whose advance produced
// post.NextIndex. The [first]-steering pick does not advance from a stored
// cursor, so its position is looked up in the sequence by the candidate key.
// Both values are 0 when no cycle is available.
func interleavedCyclePosition(pre, post interleavedstate.CycleState, c routing.AttemptCandidate) (int, int) {
	seq := post.Sequence
	if len(seq) == 0 {
		seq = pre.Sequence
	}
	n := len(seq)
	if n == 0 {
		return 0, 0
	}
	if c.MarkedFirst {
		for i, e := range seq {
			if e.Key == c.Key {
				return i + 1, n
			}
		}
		return 1, n
	}
	idx := (post.NextIndex - 1 + n) % n
	return idx + 1, n
}

func (e *Executor) logInterleavedRouteSelected(ctx context.Context, traceID, bLegID string, c routing.AttemptCandidate, pre, post interleavedstate.CycleState) {
	if e == nil || !e.interleavedEnabled() || c.InterleavedRole == interleavedstate.RoleNone {
		return
	}
	cycleIndex, cycleTotal := interleavedCyclePosition(pre, post, c)
	diag.LogInterleavedTransition(
		ctx, e.Log, "interleaved_route_selected", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
		diag.InterleavedTransition{
			Phase:      interleavedPhaseForRole(c.InterleavedRole),
			Role:       string(c.InterleavedRole),
			CycleIndex: cycleIndex,
			CycleTotal: cycleTotal,
			Target:     strings.TrimSpace(c.Key),
		},
	)
}

func (e *Executor) logInterleavedMemoShape(ctx context.Context, traceID, bLegID string, c routing.AttemptCandidate, shapeRes interleavedthinking.ShapeResult) {
	if e == nil || !e.interleavedEnabled() || c.InterleavedRole != interleavedstate.RoleExecutor {
		return
	}
	switch shapeRes.MemoOutcome {
	case interleavedthinking.MemoOutcomeInjected:
		turns := 0
		if shapeRes.MemoUpdate != nil {
			turns = shapeRes.MemoUpdate.State.RegularTurnsRemaining
		}
		diag.LogInterleavedTransition(
			ctx, e.Log, "interleaved_memo_injected", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
			diag.InterleavedTransition{
				Phase:          "executor",
				Role:           string(c.InterleavedRole),
				MemoPresent:    true,
				MemoInjected:   true,
				InjectionMode:  interleavedthinking.MemoInjectionModeTailAnchored,
				TurnsRemaining: turns,
			},
		)
	case interleavedthinking.MemoOutcomeExpired:
		diag.LogInterleavedTransition(
			ctx, e.Log, "interleaved_memo_expired", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
			diag.InterleavedTransition{Phase: "executor", Role: string(c.InterleavedRole), MemoPresent: true, MemoExpired: true},
		)
	case interleavedthinking.MemoOutcomeSkippedVisible,
		interleavedthinking.MemoOutcomeSkippedDuplicate,
		interleavedthinking.MemoOutcomeSkippedMissing,
		interleavedthinking.MemoOutcomeSkippedEmpty:
		diag.LogInterleavedTransition(
			ctx, e.Log, "interleaved_memo_skipped", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
			diag.InterleavedTransition{
				Phase:      "executor",
				Role:       string(c.InterleavedRole),
				SkipReason: memoSkipReason(shapeRes.MemoOutcome),
			},
		)
	}
}

func (e *Executor) logInterleavedThinkerSuppressed(ctx context.Context, traceID string) {
	if e == nil || !e.interleavedEnabled() {
		return
	}
	diag.LogInterleavedTransition(
		ctx, e.Log, "interleaved_thinker_suppressed", diag.AttrOpts{CallID: traceID},
		diag.InterleavedTransition{ThinkerSuppressed: true},
	)
}

func (e *Executor) logInterleavedMemoCaptured(ctx context.Context, traceID string, memo interleavedthinking.MemoState) {
	if e == nil || !e.interleavedEnabled() {
		return
	}
	diag.LogInterleavedTransition(
		ctx, e.Log, "interleaved_memo_captured", diag.AttrOpts{CallID: traceID},
		diag.InterleavedTransition{
			Phase:             "thinker",
			Role:              string(interleavedstate.RoleThinker),
			MemoPresent:       strings.TrimSpace(memo.Memo) != "",
			MemoVisible:       memo.VisibleToClient,
			ExtractionSource:  strings.TrimSpace(memo.ExtractionSource),
			StreamInterrupted: memo.StreamInterrupted,
		},
	)
}

// logInterleavedMemoStoreSkipped reports a thinker memo that was not stored:
// the captured output normalized to nothing. reason is one of
// "stream_interrupted", "empty_memo", or "no_extractable_memo", mirroring the
// Python port's differentiated store-skip reasons for production debugging.
func (e *Executor) logInterleavedMemoStoreSkipped(ctx context.Context, traceID, reason string, interrupted bool) {
	if e == nil || !e.interleavedEnabled() {
		return
	}
	diag.LogInterleavedTransition(
		ctx, e.Log, "interleaved_memo_store_skipped", diag.AttrOpts{CallID: traceID},
		diag.InterleavedTransition{
			Phase:             "thinker",
			Role:              string(interleavedstate.RoleThinker),
			SkipReason:        reason,
			StreamInterrupted: interrupted,
		},
	)
}

func (e *Executor) logInterleavedPhaseTransition(ctx context.Context, traceID string) {
	if e == nil || !e.interleavedEnabled() {
		return
	}
	diag.LogInterleavedTransition(
		ctx, e.Log, "interleaved_phase_transition", diag.AttrOpts{CallID: traceID},
		diag.InterleavedTransition{Phase: "executor", Role: string(interleavedstate.RoleExecutor)},
	)
}

func (e *Executor) logInterleavedMemoPersistFailed(ctx context.Context, traceID string, err error) {
	if e == nil || e.Log == nil || err == nil {
		return
	}
	diag.LogError(ctx, e.Log, "interleaved_memo_persist_failed", diag.AttrOpts{CallID: traceID}, err)
}

func interleavedPhaseForRole(role interleavedstate.Role) string {
	switch role {
	case interleavedstate.RoleThinker:
		return "thinker"
	case interleavedstate.RoleExecutor:
		return "executor"
	default:
		return ""
	}
}

func memoSkipReason(outcome interleavedthinking.MemoOutcome) string {
	switch outcome {
	case interleavedthinking.MemoOutcomeSkippedVisible:
		return "visible"
	case interleavedthinking.MemoOutcomeSkippedDuplicate:
		return "duplicate"
	case interleavedthinking.MemoOutcomeSkippedEmpty:
		return "empty"
	case interleavedthinking.MemoOutcomeSkippedMissing:
		return "missing"
	default:
		return ""
	}
}
