package runtime

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// aLegEndMode names the one owner permitted to end the request's A-leg.
// Interleaved thinker/executor wrappers keep the A-leg open until their outer
// boundary; ordinary streams end it at their own terminal boundary.
type aLegEndMode uint8

const (
	aLegEndBase aLegEndMode = iota
	aLegEndOuter
)

// aLegEndAuthority is the one concrete lifecycle owner shared by the base
// thinker and executor terminal views of an interleaved turn. Request/attempt
// terminal state stays separate; only A-leg lifecycle and end-once truth is
// shared across those views.
type aLegEndAuthority struct {
	scope *leglifecycle.ALeg
	mode  aLegEndMode
	once  sync.Once
}

// turnTerminal owns the logical request terminal, request-lifetime
// commitment/finished facts, and A-leg end authority. Attempt terminal
// ownership deliberately remains on each replaceable attemptSession;
// Terminalize composes with an explicitly snapshotted attempt instead of
// retaining one here.
type turnTerminal struct {
	request                  *streamTerminal
	aLegEndAuthority         *aLegEndAuthority
	commitment               atomic.Bool
	completion               atomic.Bool
	accountingFinalizedState atomic.Bool

	billingClosureMu      sync.Mutex
	billingClosureSuccess bool
}

func newTurnTerminal() *turnTerminal {
	return newTurnTerminalWithALeg(nil, aLegEndBase)
}

func newTurnTerminalWithALeg(aLeg *leglifecycle.ALeg, endMode aLegEndMode) *turnTerminal {
	return &turnTerminal{
		request:          newStreamTerminal(sdkterminal.ScopeRequest),
		aLegEndAuthority: &aLegEndAuthority{scope: aLeg, mode: endMode},
	}
}

// newTurnTerminalWithSharedALeg gives a continuation its own request terminal
// while preserving one shared A-leg lifecycle/end authority for the complete
// thinker/executor turn.
func newTurnTerminalWithSharedALeg(parent *turnTerminal) *turnTerminal {
	terminal := &turnTerminal{request: newStreamTerminal(sdkterminal.ScopeRequest)}
	if parent != nil {
		terminal.aLegEndAuthority = parent.aLegEndAuthority
	}
	return terminal
}

// deferALegEndToOuter is a construction-time, one-way ownership handoff used
// by the interleaved wrapper when a stream was assembled with base ownership
// before the wrapper decision was applied. It cannot move ownership back to
// the base stream and must run before any terminal operation is exposed.
func (t *turnTerminal) deferALegEndToOuter() bool {
	if t == nil {
		return false
	}
	if t.aLegEndAuthority == nil {
		return false
	}
	if t.aLegEndAuthority.mode == aLegEndOuter {
		return true
	}
	if t.aLegEndAuthority.mode != aLegEndBase {
		return false
	}
	t.aLegEndAuthority.mode = aLegEndOuter
	return true
}

// aLegScope is a transitional construction seam for upstream open helpers.
// All lifecycle mutations and A-leg end ownership remain behind turnTerminal.
func (t *turnTerminal) aLegScope() *leglifecycle.ALeg {
	if t == nil || t.aLegEndAuthority == nil {
		return nil
	}
	return t.aLegEndAuthority.scope
}

func (t *turnTerminal) hasALeg() bool {
	return t != nil && t.aLegEndAuthority != nil && t.aLegEndAuthority.scope != nil
}

func (t *turnTerminal) aLegErr() error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.Err()
}

func (t *turnTerminal) registerBLeg(ctx context.Context, h leglifecycle.BLegHandle) error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.RegisterBLeg(ctx, h)
}

func (t *turnTerminal) cancelALeg(ctx context.Context, cause leglifecycle.CancelCause) error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.Cancel(ctx, cause)
}

func (t *turnTerminal) releaseBLeg(id string) {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return
	}
	t.aLegEndAuthority.scope.ReleaseBLeg(id)
}

// endALeg is the sole A-leg end authority. It returns true only to the caller
// that performed the once-only end. A base caller cannot end an outer-owned
// interleaved A-leg (and vice versa).
func (t *turnTerminal) endALeg(mode aLegEndMode) bool {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil || t.aLegEndAuthority.mode != mode {
		return false
	}
	ended := false
	t.aLegEndAuthority.once.Do(func() {
		t.aLegEndAuthority.scope.End()
		ended = true
	})
	return ended
}

// requestTerminal returns the request-scope stream terminal for narrow runtime
// integration assertions. Attempt ownership is intentionally not exposed here.
func (t *turnTerminal) requestTerminal() *streamTerminal {
	if t == nil {
		return nil
	}
	return t.request
}

// Committed reports the sole request-lifetime output-commit truth.
func (t *turnTerminal) committed() bool {
	return t != nil && t.commitment.Load()
}

// MarkCommitted publishes request-level commitment and sends a one-way
// notification to the explicitly snapshotted attempt authority. The attempt
// notification is intentionally not used as request truth and is repeated for
// idempotent marks so a newly current attempt can observe the commitment.
func (t *turnTerminal) markCommitted(attempt *attemptSession) {
	if t == nil {
		return
	}
	t.commitment.Store(true)
	if attempt != nil {
		attempt.authority.markOutputCommitted()
	}
}

// Finished reports whether request terminal completion has been published.
func (t *turnTerminal) finished() bool {
	return t != nil && t.completion.Load()
}

// MarkFinished publishes request completion exactly once and reports whether
// this caller performed that publication.
func (t *turnTerminal) markFinished() bool {
	return t != nil && t.completion.CompareAndSwap(false, true)
}

func (t *turnTerminal) accountingFinalized() bool {
	return t != nil && t.accountingFinalizedState.Load()
}

func (t *turnTerminal) claimAccountingFinalization() bool {
	return t != nil && t.accountingFinalizedState.CompareAndSwap(false, true)

}

func (t *turnTerminal) unclaimAccountingFinalization() {
	if t != nil {
		t.accountingFinalizedState.Store(false)
	}
}

// Terminalize composes request and explicitly snapshotted attempt ownership
// according to the command's declared scopes. Request effects are executed by
// the request winner; when a command also covers attempts, the attempt winner
// executes the same effect callback first. An idempotent attempt observation
// then allows request effects to run without repeating attempt effects, matching
// the existing runStreamTerminal behavior.
func (t *turnTerminal) terminalize(
	ctx context.Context,
	cmd sdkterminal.Command,
	attempt *attemptSession,
	snapFn func() coreterm.AccumulatorSnapshot,
	effects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	return t.terminalizeWithRequestAfter(ctx, cmd, attempt, snapFn, effects, nil)
}

// terminalizeWithRequestAfter is the composition seam used by the stream
// façade. requestAfter runs once for every request claim, including when the
// attempt has already settled or when a different attempt command conflicts;
// this preserves request-level publication/closure side effects without
// repeating attempt effects.
func (t *turnTerminal) terminalizeWithRequestAfter(
	ctx context.Context,
	cmd sdkterminal.Command,
	attempt *attemptSession,
	snapFn func() coreterm.AccumulatorSnapshot,
	effects func(context.Context, coreterm.Outcome) error,
	requestAfter func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
		if attempt == nil || attempt.terminal == nil {
			return coreterm.Result{Err: sdkterminal.ErrInvalid}
		}
		return attempt.terminal.Terminalize(ctx, cmd, snapFn, effects)
	}

	requestSnap := func() coreterm.AccumulatorSnapshot {
		var snap coreterm.AccumulatorSnapshot
		if snapFn != nil {
			snap = snapFn()
		}
		if t.committed() && !snap.OutputCommitted() {
			snap = coreterm.NewAccumulatorSnapshot(snap.Bytes(), true)
		}
		return snap
	}

	return t.request.Terminalize(ctx, cmd, requestSnap, func(cctx context.Context, out coreterm.Outcome) error {
		var effectErr error
		if cmd.AllowsScope(sdkterminal.ScopeAttempt) && attempt != nil && attempt.terminal != nil {
			attemptResult := attempt.terminal.Terminalize(cctx, cmd, func() coreterm.AccumulatorSnapshot {
				return out.Snapshot.Clone()
			}, effects)
			if attemptResult.Won {
				effectErr = attemptResult.Err
			} else if effects != nil {
				// Any non-winning attempt result (same-command observation or
				// conflict with an earlier attempt command) falls back to the
				// request effect, as in the previous nested facade.
				effectErr = effects(cctx, out)
			}
		} else if effects != nil {
			effectErr = effects(cctx, out)
		}
		if requestAfter != nil {
			if afterErr := requestAfter(cctx, out); effectErr == nil {
				effectErr = afterErr
			}
		}
		return effectErr
	})
}
