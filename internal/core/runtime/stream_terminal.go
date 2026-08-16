package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// streamTerminal binds a domain terminal.Owner to once-only effect execution.
// Concurrent Terminalize callers CAS-claim; the winner snapshots via snapFn,
// runs effects under a detached bounded cleanup context, and advances the
// owner. Losers await the same completion and observe the published outcome
// (requirements 7.1–7.3, 7.6; design D8).
type streamTerminal struct {
	mu        sync.Mutex
	owner     *coreterm.Owner
	done      chan struct{}
	closed    bool
	effectErr error
}

func newStreamTerminal(scope sdk.Scope) *streamTerminal {
	return &streamTerminal{
		owner: coreterm.NewOwner(scope),
		done:  make(chan struct{}),
	}
}

// Owner returns the underlying domain owner for evidence and tests.
func (t *streamTerminal) Owner() *coreterm.Owner {
	if t == nil {
		return nil
	}
	return t.owner
}

// Terminalize competes for the single terminal outcome on this session.
// effects may return an error; on error or panic the owner advances to Failed
// and all waiters observe the same Err.
func (t *streamTerminal) Terminalize(
	ctx context.Context,
	cmd sdk.Command,
	snapFn func() coreterm.AccumulatorSnapshot,
	effects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.owner == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	if snapFn == nil {
		snapFn = func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot(nil, false)
		}
	}

	t.mu.Lock()
	snap := snapFn()
	r := t.owner.Claim(cmd, snap)
	won := r.Won
	t.mu.Unlock()

	if !won {
		if shouldAwaitTerminalResult(r) {
			<-t.done
			if out, ok := t.owner.Outcome(); ok {
				r.Outcome = out
			}
			r.State = t.owner.State()
			t.mu.Lock()
			effectErr := t.effectErr
			t.mu.Unlock()
			if effectErr != nil {
				r.Err = effectErr
			}
		}
		return r
	}

	defer t.signalDone()

	cleanupCtx, cancel := cleanupContext(ctx, defaultAuthorityCleanupTimeout)
	defer cancel()

	var effectErr error
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		if effects != nil {
			effectErr = effects(cleanupCtx, r.Outcome)
		}
	}()

	t.mu.Lock()
	t.effectErr = effectErr
	t.mu.Unlock()

	durablePending := errors.Is(effectErr, terminalworkapp.ErrDurablePending)
	if panicked || (effectErr != nil && !durablePending) || cmd == sdk.CommandPanic {
		_ = t.owner.Advance(sdk.StateFailed)
		r.State = t.owner.State()
		if effectErr != nil {
			r.Err = effectErr
		} else if panicked {
			r.Err = errors.New("runtime: stream terminal effect panic")
		}
		return r
	}

	if durablePending {
		// Durable intent accepted: keep output commitment and stop at work_pending
		// (requirements 7.7, 8.3; design D8/D9).
		if err := t.owner.Advance(sdk.StateWorkPending); err != nil {
			_ = t.owner.Advance(sdk.StateFailed)
		}
		r.State = t.owner.State()
		r.Err = effectErr
		return r
	}

	for _, st := range []sdk.State{
		sdk.StateWorkPending,
		sdk.StateSettled,
		sdk.StateReleasePending,
		sdk.StateReleased,
	} {
		if err := t.owner.Advance(st); err != nil {
			_ = t.owner.Advance(sdk.StateFailed)
			break
		}
	}
	r.State = t.owner.State()
	return r
}

// shouldAwaitTerminalResult awaits when a competing claim exists. Initial
// open+committed GateReplacement rejection (StateOpen + ErrOutputCommitted)
// returns immediately without awaiting.
func shouldAwaitTerminalResult(r coreterm.Result) bool {
	if r.Err == nil || errors.Is(r.Err, sdk.ErrConflict) {
		return true
	}
	if errors.Is(r.Err, sdk.ErrOutputCommitted) && r.State != sdk.StateOpen {
		return true
	}
	return false
}

func (t *streamTerminal) signalDone() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	close(t.done)
	t.closed = true
}

type accumulatorSnapWire struct {
	Events int    `json:"e"`
	Text   string `json:"t,omitempty"`
	Final  bool   `json:"f"`
}

// snapshotTerminals returns coherent request/attempt owners, initializing them
// once under termMu. Callers must use the returned pointers for the duration of
// a terminalize attempt and must not hold termMu across Terminalize/effects.
func (s *retryRecvStream) snapshotTerminals() (req, att *streamTerminal) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if s.requestTerm == nil {
		s.requestTerm = newStreamTerminal(sdk.ScopeRequest)
	}
	if s.attemptTerm == nil {
		s.attemptTerm = newStreamTerminal(sdk.ScopeAttempt)
	}
	return s.requestTerm, s.attemptTerm
}

func (s *retryRecvStream) ensureTerminals() {
	if s == nil {
		return
	}
	_, _ = s.snapshotTerminals()
}

// resetAttemptTerminal replaces attempt ownership for a replacement transition.
// In-flight Close/Recv that already snapshotted keep their prior attempt owner.
func (s *retryRecvStream) resetAttemptTerminal() {
	if s == nil {
		return
	}
	s.termMu.Lock()
	s.attemptTerm = newStreamTerminal(sdk.ScopeAttempt)
	s.termMu.Unlock()
}

func (s *retryRecvStream) accumulatorSnapshot() coreterm.AccumulatorSnapshot {
	if s == nil {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	w := accumulatorSnapWire{
		Events: len(s.seenEvents),
		Final:  s.tokenAccountingFinalized,
	}
	if s.customer != nil {
		w.Text, _, _, _ = s.customer.Snapshot()
	} else {
		w.Text = s.visibleText.String()
	}
	raw, _ := json.Marshal(w)
	return coreterm.NewAccumulatorSnapshot(raw, s.isCommitted())
}

func (s *retryRecvStream) seenEventsCopy() []lipapi.Event {
	if s == nil {
		return nil
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return append([]lipapi.Event(nil), s.seenEvents...)
}

func (s *retryRecvStream) clearClientAccumulators() {
	if s == nil {
		return
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	s.seenEvents = nil
	s.visibleText.Reset()
}

// runStreamTerminal claims the request owner (when legal) and nested attempt
// owner, then runs effects once under the winner. Attempt-only / request-only
// commands skip the nested plane. Losers await completion without re-running effects.
func (s *retryRecvStream) runStreamTerminal(
	ctx context.Context,
	cmd sdk.Command,
	effects func(context.Context) error,
) coreterm.Result {
	if s == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	req, att := s.snapshotTerminals()
	snapFn := func() coreterm.AccumulatorSnapshot { return s.accumulatorSnapshot() }

	runEffects := func(cctx context.Context, _ coreterm.Outcome) error {
		if effects == nil {
			return nil
		}
		return effects(cctx)
	}

	if !cmd.AllowsScope(sdk.ScopeRequest) {
		return att.Terminalize(ctx, cmd, snapFn, runEffects)
	}
	r := req.Terminalize(ctx, cmd, snapFn, func(cctx context.Context, out coreterm.Outcome) error {
		var err error
		if cmd.AllowsScope(sdk.ScopeAttempt) {
			ar := att.Terminalize(cctx, cmd, func() coreterm.AccumulatorSnapshot {
				return out.Snapshot.Clone()
			}, runEffects)
			if ar.Won {
				err = ar.Err
			} else {
				err = runEffects(cctx, out)
			}
		} else {
			err = runEffects(cctx, out)
		}
		s.recordBillingLeg(cctx, cmd)
		s.handoffBillingTurn(cctx, cmd)
		return err
	})
	// Committed GateReplacement cannot take ownership (D13) but still freezes
	// call-closure: no further B-leg can be allocated, and TUR/retry stay off.
	if !r.Won && cmd == sdk.CommandGateReplacement && errors.Is(r.Err, sdk.ErrOutputCommitted) {
		s.recordBillingLeg(ctx, cmd)
		s.handoffBillingTurn(ctx, cmd)
	}
	return r
}

// runAttemptTerminal claims only the attempt owner (swallowed/parallel/open seams).
func (s *retryRecvStream) runAttemptTerminal(
	ctx context.Context,
	cmd sdk.Command,
	effects func(context.Context) error,
) coreterm.Result {
	if s == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	_, att := s.snapshotTerminals()
	return att.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return s.accumulatorSnapshot()
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		var err error
		if effects != nil {
			err = effects(cctx)
		}
		s.recordBillingLeg(cctx, cmd)
		return err
	})
}

// terminalizeAttemptEphemeral runs attempt-scoped terminalization without a stream
// (parallel losers, pre-backend/backend-open cleanup).
func terminalizeAttemptEphemeral(
	ctx context.Context,
	cmd sdk.Command,
	outputCommitted bool,
	effects func(context.Context) error,
) coreterm.Result {
	term := newStreamTerminal(sdk.ScopeAttempt)
	return term.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, outputCommitted)
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		if effects == nil {
			return nil
		}
		return effects(cctx)
	})
}

// terminalLossError maps a lost terminal claim to the error Recv/finish paths surface.
func terminalLossError(r coreterm.Result) error {
	if r.Err != nil && !errors.Is(r.Err, sdk.ErrConflict) && !errors.Is(r.Err, sdk.ErrOutputCommitted) {
		return r.Err
	}
	switch r.Outcome.Command {
	case sdk.CommandClose, sdk.CommandCancel:
		// Match Close/a-leg cancel vocabulary already used by Recv cancel paths.
		return leglifecycle.ErrALegCanceled
	case sdk.CommandTimeout:
		return context.DeadlineExceeded
	case sdk.CommandEOF:
		return io.EOF
	case "":
		if r.Err != nil {
			return r.Err
		}
		return errors.New("runtime: stream already terminalized")
	default:
		if r.Err != nil {
			return r.Err
		}
		return fmt.Errorf("runtime: stream terminalized by %s", r.Outcome.Command)
	}
}
