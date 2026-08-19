package runtime

import (
	"context"
	"sync/atomic"

	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// turnTerminal owns the logical request terminal and the request-lifetime
// commitment/finished facts. Attempt terminal ownership deliberately remains
// on each replaceable attemptSession; Terminalize composes with an explicitly
// snapshotted attempt instead of retaining one here.
type turnTerminal struct {
	request    *streamTerminal
	commitment atomic.Bool
	completion atomic.Bool
}

func newTurnTerminal() *turnTerminal {
	return &turnTerminal{request: newStreamTerminal(sdkterminal.ScopeRequest)}
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
