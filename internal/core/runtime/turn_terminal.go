package runtime

import (
	"context"
	"errors"
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

// terminalizeSnapshot composes request and explicitly captured attempt
// ownership according to the command's declared scopes. The evidence snapshot
// and attempt pointer are values supplied by the caller before any terminal
// work starts; this prevents a replacement from changing the B-leg attributed
// to an in-flight terminal effect.
//
// attemptEffects are the attempt-local effects. requestEffects are the
// request-after effects (including request-level economic closure). Keeping the
// pair explicit avoids a broad effects bag while preserving the old nested
// terminal semantics: an attempt winner runs attemptEffects once, an attempt
// loser falls back to the request invocation, and requestEffects runs once for
// the request winner.
func (t *turnTerminal) terminalizeSnapshot(
	ctx context.Context,
	cmd sdkterminal.Command,
	attempt *attemptSession,
	snapshot coreterm.AccumulatorSnapshot,
	attemptEffects func(context.Context, coreterm.Outcome) error,
	requestEffects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
		if attempt == nil {
			return coreterm.Result{Err: sdkterminal.ErrInvalid}
		}
		return attempt.terminalizeSnapshot(ctx, cmd, snapshot, attemptEffects)
	}

	if t.committed() && !snapshot.OutputCommitted() {
		snapshot = coreterm.NewAccumulatorSnapshot(snapshot.Bytes(), true)
	}

	r := t.request.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return snapshot.Clone()
	}, func(cctx context.Context, out coreterm.Outcome) error {
		var effectErr error
		if cmd.AllowsScope(sdkterminal.ScopeAttempt) && attempt != nil {
			attemptResult := attempt.terminalizeSnapshot(cctx, cmd, out.Snapshot, attemptEffects)
			if attemptResult.Won {
				effectErr = attemptResult.Err
			} else if attemptEffects != nil {
				// A settled/conflicting attempt still allows the request owner to
				// apply the request-scoped fallback effect once.
				effectErr = attemptEffects(cctx, out)
			}
		} else if attemptEffects != nil {
			effectErr = attemptEffects(cctx, out)
		}
		if requestEffects != nil {
			if requestErr := requestEffects(cctx, out); effectErr == nil {
				effectErr = requestErr
			}
		}
		return effectErr
	})
	// A committed gate replacement cannot claim the request owner, but it still
	// closes the request-level billing/economic window. The closure owner
	// supplies its own once/dedupe guard, so competing rejected gate attempts
	// may safely invoke this narrow request-after seam.
	if !r.Won && cmd == sdkterminal.CommandGateReplacement && errors.Is(r.Err, sdkterminal.ErrOutputCommitted) && requestEffects != nil {
		_ = requestEffects(ctx, r.Outcome)
	}
	return r
}

// terminalizeSnapshot is the attempt owner operation used by turnTerminal and
// by explicit attempt-retirement paths. It accepts a captured evidence value
// and never consults the mutable attempt slot.
func (a *attemptSession) terminalizeSnapshot(
	ctx context.Context,
	cmd sdkterminal.Command,
	snapshot coreterm.AccumulatorSnapshot,
	effects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if a == nil || a.terminal == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	return a.terminal.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return snapshot.Clone()
	}, effects)
}
