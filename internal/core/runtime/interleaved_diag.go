package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

func (e *Executor) logInterleavedRouteSelected(ctx context.Context, traceID, bLegID string, c routing.AttemptCandidate) {
	if e == nil || !e.interleavedEnabled() || c.InterleavedRole == interleavedstate.RoleNone {
		return
	}
	diag.LogInterleavedTransition(ctx, e.Log, "interleaved_route_selected", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
		diag.InterleavedTransition{
			Phase: interleavedPhaseForRole(c.InterleavedRole),
			Role:  string(c.InterleavedRole),
		},
	)
}

func (e *Executor) logInterleavedMemoShape(ctx context.Context, traceID, bLegID string, c routing.AttemptCandidate, shapeRes interleavedthinking.ShapeResult) {
	if e == nil || !e.interleavedEnabled() || c.InterleavedRole != interleavedstate.RoleExecutor {
		return
	}
	switch shapeRes.MemoOutcome {
	case interleavedthinking.MemoOutcomeInjected:
		diag.LogInterleavedTransition(ctx, e.Log, "interleaved_memo_injected", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
			diag.InterleavedTransition{Phase: "executor", Role: string(c.InterleavedRole), MemoPresent: true, MemoInjected: true},
		)
	case interleavedthinking.MemoOutcomeExpired:
		diag.LogInterleavedTransition(ctx, e.Log, "interleaved_memo_expired", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
			diag.InterleavedTransition{Phase: "executor", Role: string(c.InterleavedRole), MemoPresent: true, MemoExpired: true},
		)
	case interleavedthinking.MemoOutcomeSkippedVisible,
		interleavedthinking.MemoOutcomeSkippedDuplicate,
		interleavedthinking.MemoOutcomeSkippedMissing,
		interleavedthinking.MemoOutcomeSkippedEmpty:
		diag.LogInterleavedTransition(ctx, e.Log, "interleaved_memo_skipped", diag.AttrOpts{CallID: traceID, BLegID: bLegID},
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
	diag.LogInterleavedTransition(ctx, e.Log, "interleaved_thinker_suppressed", diag.AttrOpts{CallID: traceID},
		diag.InterleavedTransition{ThinkerSuppressed: true},
	)
}

func (e *Executor) logInterleavedMemoCaptured(ctx context.Context, traceID string, memo interleavedthinking.MemoState) {
	if e == nil || !e.interleavedEnabled() {
		return
	}
	diag.LogInterleavedTransition(ctx, e.Log, "interleaved_memo_captured", diag.AttrOpts{CallID: traceID},
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

func (e *Executor) logInterleavedPhaseTransition(ctx context.Context, traceID string) {
	if e == nil || !e.interleavedEnabled() {
		return
	}
	diag.LogInterleavedTransition(ctx, e.Log, "interleaved_phase_transition", diag.AttrOpts{CallID: traceID},
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
