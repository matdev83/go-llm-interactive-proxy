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

// snapshotTerminals returns coherent request/attempt owners. Production
// construction installs the turn terminal and attempt before the stream is
// exposed; an unconstructed direct fixture returns nil and is rejected by the
// terminal call sites until its explicit test owner is installed.
func (s *retryRecvStream) snapshotTerminals() (req, att *streamTerminal) {
	if s == nil {
		return nil, nil
	}
	if s.terminal == nil {
		return nil, nil
	}
	request := s.terminal.requestTerminal()
	attempt := s.attempt.snapshot()
	if attempt != nil {
		return request, attempt.terminal
	}
	return request, nil
}

func (s *retryRecvStream) accumulatorSnapshot() coreterm.AccumulatorSnapshot {
	if s == nil {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	w := accumulatorSnapWire{
		Events: len(s.seenEvents),
		Final:  s.terminal != nil && s.terminal.accountingFinalized(),
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
	return s.runStreamTerminalForAttempt(ctx, cmd, s.attempt.snapshot(), effects)
}

// runStreamTerminalForAttempt is the explicit-attempt seam used when an
// economic terminal effect has already captured its B-leg. It never consults
// the mutable attempt slot after that capture.
func (s *retryRecvStream) runStreamTerminalForAttempt(
	ctx context.Context,
	cmd sdk.Command,
	attempt *attemptSession,
	effects func(context.Context) error,
) coreterm.Result {
	if s == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	if s.terminal == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	snapFn := func() coreterm.AccumulatorSnapshot { return s.accumulatorSnapshot() }

	runEffects := func(cctx context.Context, _ coreterm.Outcome) error {
		if effects == nil {
			return nil
		}
		return effects(cctx)
	}

	r := s.terminal.terminalizeWithRequestAfter(ctx, cmd, attempt, snapFn, func(cctx context.Context, out coreterm.Outcome) error {
		err := runEffects(cctx, out)
		return err
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		if !cmd.AllowsScope(sdk.ScopeRequest) {
			return nil
		}
		s.recordBillingLegForAttempt(cctx, attempt, cmd)
		s.handoffBillingTurn(cctx, cmd)
		return nil
	})
	// Committed GateReplacement cannot take ownership (D13) but still freezes
	// call-closure: no further B-leg can be allocated, and TUR/retry stay off.
	if !r.Won && cmd == sdk.CommandGateReplacement && errors.Is(r.Err, sdk.ErrOutputCommitted) {
		s.recordBillingLegForAttempt(ctx, attempt, cmd)
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
	att := s.attempt.snapshot()
	return s.runAttemptTerminalForAttempt(ctx, cmd, att, effects)
}

func (s *retryRecvStream) runAttemptTerminalForAttempt(
	ctx context.Context,
	cmd sdk.Command,
	att *attemptSession,
	effects func(context.Context) error,
) coreterm.Result {
	if s == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	if att == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	if att.terminal == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	return att.terminal.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return s.accumulatorSnapshot()
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		var err error
		if effects != nil {
			err = effects(cctx)
		}
		s.recordBillingLegForAttempt(cctx, att, cmd)
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
