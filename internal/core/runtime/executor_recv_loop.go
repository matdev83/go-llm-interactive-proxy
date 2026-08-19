package runtime

// Recv-phase inner-loop control for retryRecvStream. Stream lifecycle
// helpers (Close, handleRecvSuccess, handleRecvEOF,
// etc.) remain in executor_retry_stream.go; this file owns the inner-loop
// state machine that drives per-recv failover within an attempt's budget.

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// errGateContinueInner signals Recv to pull another inner event without returning to the client yet.
var errGateContinueInner = errors.New("runtime: completion gate continue buffering")

func (s *retryRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s == nil {
		return lipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if s.terminal.finished() {
		return lipapi.Event{}, io.EOF
	}
	attempt := s.attempt.require()
	ctx = s.recvExecContext(ctx)
	if err := ctx.Err(); err != nil {
		if s.terminal.finished() {
			return lipapi.Event{}, err
		}
		if inner := attempt.loadInner(); inner != nil {
			s.consumeBackendUsageEvidenceForAttempt(ctx, attempt, inner)
			ev, _, herr := s.handleRecvError(ctx, ctx, err, idleContextDeadline{}, ttftContextDeadline{})
			if herr != nil {
				return ev, herr
			}
			return lipapi.Event{}, err
		}
		if !attempt.authority.Settled() {
			attempt.authority.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindSwallowed, s.responsePipeline.operatorUsageForFinalize())
		}
		reason := cancellationAttemptReason(ctx, err)
		if s.responsePipeline != nil {
			attempt.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptCancelled,
				Reason:    reason,
				DetailErr: err,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
		}
		cmd := sdkterminal.CommandCancel
		if errors.Is(err, context.DeadlineExceeded) {
			cmd = sdkterminal.CommandTimeout
		}
		s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.responsePipeline.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
			s.persistCancellationBilling(cctx, attempt, reason)
			s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeCancelled)
			s.markFinished()
			return nil
		}, func(cctx context.Context, _ coreterm.Outcome) error {
			s.terminal.recordBillingLegForAttempt(cctx, s.facts, attempt, cmd, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed())
			s.terminal.handoffBillingTurn(cctx, s.facts, cmd)
			return nil
		})
		if !s.terminal.finished() {
			s.markFinished()
		}
		s.terminal.endALeg(aLegEndBase)
		return lipapi.Event{}, err
	}
	if ev, hasRecoveryDrain := s.responsePipeline.popRecoveryDrain(); hasRecoveryDrain {
		if ev.Kind == lipapi.EventResponseFinished && (s.terminal == nil || !s.terminal.accountingFinalized()) {
			recording := s.responsePipeline.recordClientFacing(ctx, s.facts, attempt, ev, s.terminal.committed())
			if recording.mandatory() {
				s.responsePipeline.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
				s.terminalizePartialFailure(ctx, sdkterminal.CommandFrontendEncoderFailure, attemptReasonDetail(recording.err), recording.err)
				return lipapi.Event{}, recording.err
			}
			usageEv, ok, err := s.finalizeResponseFinishedAuthority(ctx, ev)
			if err != nil {
				if !s.terminal.finished() {
					s.markFinished()
				}
				return lipapi.Event{}, err
			}
			if ok {
				s.responsePipeline.prependRecoveryDrain(ev)
				return s.emitSynthesizedUsage(ctx, usageEv)
			}
		}
		if ev.Kind == lipapi.EventResponseFinished {
			s.markFinished()
			s.terminal.endALeg(aLegEndBase)
		}
		pm, _ := s.recvHookMeta()
		out, recording, emitErr := s.responsePipeline.observeClientFacing(ctx, ev, responseEventInput{
			facts: s.facts, attempt: attempt, recovery: s.recovery,
			pm: pm, committed: s.terminal.committed(), now: s.responsePipeline.nowTime(), recorded: ev.Kind == lipapi.EventResponseFinished,
			finishBeforeRelease: true,
		})
		if emitErr != nil {
			cmd := sdkterminal.CommandPartialError
			if recording.mandatory() {
				cmd = sdkterminal.CommandFrontendEncoderFailure
			}
			s.terminalizePartialFailure(ctx, cmd, attemptReasonDetail(emitErr), emitErr)
		}
		if emitErr == nil && lipapi.OutputCommitted(out) {
			s.markOutputCommitted(out)
		}
		return out, emitErr
	}
	for {
		// Replacement installs a new attempt session while this Recv call
		// continues. Refresh the short-lived snapshot before any attempt-local
		// receive or terminal decision; never carry the retired B-leg identity
		// into the replacement.
		attempt = s.attempt.require()
		if attempt.toolFinal != nil {
			if ev, ok := attempt.toolFinal.popDrain(); ok {
				out, cont, err := s.dispatchClientFacingEvent(ctx, ev)
				if cont {
					continue
				}
				return out, err
			}
		}
		if ev, ok := s.responsePipeline.popGateDrainHead(); ok {
			// A gate-drain finish is finalized through the same centralized chokepoint as the other
			// response_finished completion paths, before emitGateDrained marks the stream finished, so
			// a reconstructed-usage (ok) result can re-queue the finish and emit the synthesized
			// usage_delta without stranding the finish behind a finished stream. The non-ok result
			// falls through to emitGateDrained + the standard client-event emit. Without this the
			// gate-drain site leaked its reserved authority (it had no finalization at all before
			// centralization).
			if ev.Kind == lipapi.EventResponseFinished && (s.terminal == nil || !s.terminal.accountingFinalized()) {
				recording := s.responsePipeline.recordClientFacing(ctx, s.facts, attempt, ev, s.terminal.committed())
				if recording.mandatory() {
					s.responsePipeline.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
					s.terminalizePartialFailure(ctx, sdkterminal.CommandFrontendEncoderFailure, attemptReasonDetail(recording.err), recording.err)
					return lipapi.Event{}, recording.err
				}
				usageEv, usageOk, err := s.finalizeResponseFinishedAuthority(ctx, ev)
				if err != nil {
					if !s.terminal.finished() {
						s.markFinished()
					}
					return lipapi.Event{}, err
				}
				if usageOk {
					s.responsePipeline.prependRecoveryDrain(ev)
					emitted, emitErr := s.emitSynthesizedUsage(ctx, usageEv)
					return emitted, emitErr
				}
			}
			if lipapi.OutputCommitted(ev) {
				s.terminal.markCommitted(s.attempt.snapshot())
			}
			if ev.Kind == lipapi.EventResponseFinished {
				if s.responsePipeline != nil {
					attempt.recordAttemptLogged(ctx, recordAttemptParams{
						ALegID: s.facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess,
					}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
				}
				s.markFinished()
			}
			attempt.accounting.observeClientEvent(s.responsePipeline.nowTime(), ev)
			pm, _ := s.recvHookMeta()
			out, recording, emitErr := s.responsePipeline.observeClientFacing(ctx, ev, responseEventInput{
				facts: s.facts, attempt: attempt, recovery: s.recovery,
				pm: pm, committed: s.terminal.committed(), now: s.responsePipeline.nowTime(), recorded: ev.Kind == lipapi.EventResponseFinished,
				finishBeforeRelease: true,
			})
			if emitErr != nil {
				cmd := sdkterminal.CommandPartialError
				if recording.mandatory() {
					cmd = sdkterminal.CommandFrontendEncoderFailure
				}
				s.terminalizePartialFailure(ctx, cmd, attemptReasonDetail(emitErr), emitErr)
			}
			if emitErr == nil && lipapi.OutputCommitted(out) {
				s.markOutputCommitted(out)
			}
			return out, emitErr
		}
		var inner lipapi.ManagedEventStream
		for {
			attempt = s.attempt.require()
			inner = attempt.loadInner()
			if inner != nil {
				break
			}
			opened, err := s.tryReplacementIteration(ctx)
			if err != nil {
				// tryReplacementIteration releases the prior (swallowed) attempt's
				// authority reservation before opening the replacement, so on most
				// error paths it is already settled. The early-return guards
				// (ctx.Err, aScope.Err, secure-recording hard stop) return before
				// that release, so release it here when it has not already been
				// settled, then tear down the stream like the other terminal recv exits.
				if !attempt.authority.Settled() {
					attempt.terminalizeSnapshot(ctx, sdkterminal.CommandSwallowedAttempt, s.responsePipeline.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
						attempt.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, s.responsePipeline.operatorUsageForFinalize())
						s.terminal.recordBillingLegForAttempt(cctx, s.facts, attempt, sdkterminal.CommandSwallowedAttempt, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed())
						return nil
					})
				}
				s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandPartialError, attempt, s.responsePipeline.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
					s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
					s.markFinished()
					return nil
				}, func(cctx context.Context, _ coreterm.Outcome) error {
					s.terminal.recordBillingLegForAttempt(cctx, s.facts, attempt, sdkterminal.CommandPartialError, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed())
					s.terminal.handoffBillingTurn(cctx, s.facts, sdkterminal.CommandPartialError)
					return nil
				})
				if !s.terminal.finished() {
					s.markFinished()
				}
				s.terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}
			if !opened {
				return stream.DefaultKeepaliveEvent(), nil
			}
		}
		attempt = s.attempt.require()
		// Connector sideband frames can arrive after Open returns. Drain immediately
		// before each receive so pre-first-event evidence is accounted even when the
		// transport reports its first read error or cancellation.
		s.consumeBackendUsageEvidenceForAttempt(ctx, attempt, inner)
		recvCtx := ctx
		var cancelRecv context.CancelFunc = func() {}
		ttftDeadline := ttftContextDeadline{}
		if !s.terminal.committed() && s.recovery != nil && s.recovery.ttft != nil {
			recvCtx, cancelRecv, ttftDeadline = s.recovery.ttft.scopedContext(ctx, s.responsePipeline.nowTime(), attempt.cand.Key, attempt.cand.Primary.TTFTTimeout)
		}
		recvCtx, cancelRecv, idleDeadline := s.scopedIdleContext(recvCtx, cancelRecv, s.responsePipeline.nowTime())
		ev, err := safety.CallValue(safety.BoundaryBackend, "backend_recv", func() (lipapi.Event, error) {
			return inner.Recv(recvCtx)
		})
		cancelRecv()
		// Evidence may be published during the receive itself. Drain after the
		// call so a final event, EOF, or error cannot discard that evidence.
		s.consumeBackendUsageEvidenceForAttempt(ctx, attempt, inner)
		// Close/cancel may have terminalized while we were blocked. Do not run
		// NormalFinish (or surface bare context.Canceled) after that owner won.
		if s.terminal.finished() {
			if s.terminal != nil && s.terminal.hasALeg() {
				if scopeErr := s.terminal.aLegErr(); errors.Is(scopeErr, leglifecycle.ErrALegCanceled) {
					return lipapi.Event{}, scopeErr
				}
			}
			return lipapi.Event{}, io.EOF
		}
		if err != nil {
			var pe *safety.PanicError
			if errors.As(err, &pe) {
				err = mapStreamPanic(pe, s.terminal.committed())
			}
		}
		if err != nil && s.terminal != nil && s.terminal.hasALeg() {
			if scopeErr := s.terminal.aLegErr(); errors.Is(scopeErr, leglifecycle.ErrALegCanceled) {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.facts.aLegID,
					BLeg:      attempt.bleg,
					Cand:      attempt.cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "a-leg canceled",
					DetailErr: scopeErr,
				}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
				_ = attempt.takeInner()
				s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandCancel, attempt, s.responsePipeline.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
					s.persistCancellationBilling(cctx, attempt, "a-leg canceled")
					s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeCancelled)
					s.markFinished()
					return nil
				}, func(cctx context.Context, _ coreterm.Outcome) error {
					s.terminal.recordBillingLegForAttempt(cctx, s.facts, attempt, sdkterminal.CommandCancel, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed())
					s.terminal.handoffBillingTurn(cctx, s.facts, sdkterminal.CommandCancel)
					return nil
				})
				if !s.terminal.finished() {
					s.markFinished()
				}
				s.terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, scopeErr
			}
		}
		if err == nil {
			ev, cont, err := s.handleRecvSuccess(ctx, ev)
			if cont {
				continue
			}
			return ev, err
		}
		if errors.Is(err, io.EOF) {
			return s.handleRecvEOF(ctx)
		}
		ev, cont, err := s.handleRecvError(ctx, recvCtx, err, idleDeadline, ttftDeadline)
		if cont {
			continue
		}
		return ev, err
	}
}

// tryReplacementIteration performs one planning + open attempt for recv-phase failover.
// It returns opened=true when the active attempt stream is ready, opened=false when the caller should emit
// a keepalive (Req 5.5) and invoke Recv again, or a non-nil error when the replacement path is exhausted.
func (s *retryRecvStream) tryReplacementIteration(ctx context.Context) (opened bool, err error) {
	attempt := s.attempt.require()
	ctx = diag.EnsureCallDiag(ctx, s.facts.traceID, s.facts.aLegID)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.recovery != nil && s.recovery.turnCommitted(s.terminal) && s.responsePipeline.recordingBlocksReplacement() && s.responsePipeline != nil && s.responsePipeline.secureRecordingMandatory {
		// Output committed: compete for gate-replacement rejection evidence (D13) without effects.
		_ = s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandGateReplacement, attempt, s.responsePipeline.accumulatorSnapshot(), nil, func(cctx context.Context, _ coreterm.Outcome) error {
			s.terminal.recordBillingLegForAttempt(cctx, s.facts, attempt, sdkterminal.CommandGateReplacement, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed())
			s.terminal.handoffBillingTurn(cctx, s.facts, sdkterminal.CommandGateReplacement)
			return nil
		})
		return false, &lipapi.UpstreamFailureError{
			Phase:        lipapi.PhasePostOutput,
			Recoverable:  false,
			Reason:       "secure session mandatory recorder failure after committed output",
			CandidateKey: strings.TrimSpace(attempt.cand.Key),
		}
	}
	if s.terminal != nil && s.terminal.hasALeg() {
		if err := s.terminal.aLegErr(); err != nil {
			return false, err
		}
	}
	// Release the prior (swallowed) attempt's reservation BEFORE opening and
	// authoritatively admitting the replacement. Releasing after the admit (the
	// previous ordering) left both reservations overlapping the same live window,
	// so strict quota/rate/budget enforcement could reject the replacement with
	// ErrReservationConflict or double-count capacity even though it is the same
	// logical request continuing after a swallowed B-leg. A settled prior
	// (e.g. after a failed partial settle's losing-release) is a no-op. Reset
	// below swaps in the freshly opened reservation and clears the settled guard.
	if !attempt.authority.Settled() {
		finalize := func(cctx context.Context) error {
			attempt.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, s.responsePipeline.operatorUsageForFinalize())
			return nil
		}
		// Recoverable recv errors may already have consumed the attempt terminal
		// while recording the swallowed attempt. The terminal is never reset;
		// finish authority directly in that case. Direct replacement callers still
		// claim the fresh attempt terminal before applying the same effects.
		if owner := attempt.terminal.Owner(); owner != nil && owner.State() == sdkterminal.StateOpen {
			attempt.terminalizeSnapshot(ctx, sdkterminal.CommandSwallowedAttempt, s.responsePipeline.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
				err := finalize(cctx)
				s.terminal.recordBillingLegForAttempt(cctx, s.facts, attempt, sdkterminal.CommandSwallowedAttempt, s.responsePipeline.billingEvidenceFallback(), s.terminal.committed())
				return err
			})
		} else {
			_ = finalize(ctx)
		}
	}
	if s.recovery == nil {
		return false, errors.New("runtime: recv recovery controller unavailable")
	}
	out, err := s.recovery.openReplacement(ctx, s.facts, s.terminal, attempt)
	if err != nil {
		return false, err
	}
	if !out.opened {
		return false, nil
	}
	next := s.recovery.buildReplacementAttempt(out, s.facts)
	if next == nil {
		return false, errors.New("runtime: replacement attempt construction unavailable")
	}
	if s.terminal != nil && !out.registered {
		if err := s.terminal.registerBLeg(ctx, leglifecycle.BLegHandle{
			ID:      out.bleg.BLegID,
			Attempt: lifecycleAttempt(out.stream),
		}); err != nil {
			if out.stream != nil && !errors.Is(err, leglifecycle.ErrALegCanceled) {
				_ = out.stream.Close()
			}
			// out.authority was freshly admitted for this replacement attempt and
			// is not yet assigned to the stream's attempt session (that happens only on the success
			// path below), so release it here to avoid leaking the reservation. The
			// prior swallowed attempt was already released before tryPlanOpenOnce.
			_ = terminalizeAttemptEphemeral(ctx, sdkterminal.CommandSwallowedAttempt, false, func(cctx context.Context) error {
				next.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, emptyOperatorUsageShell())
				return nil
			})
			if s.recovery.postOpenLeg != nil {
				s.recovery.postOpenLeg(ctx, s.facts.billingCallState, s.facts.aLegID, out.bleg, out.cand.Primary, time.Time{}, time.Time{})
			}
			return false, err
		}
	}
	clearAttemptToolState(s.responsePipeline, s.attempt.snapshot())
	if _, published := s.attempt.swapIfOpen(next); !published {
		cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
		freshInner := next.takeInner()
		if freshInner != nil {
			s.cancelAndCloseInner(cleanupCtx, freshInner, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		}
		_ = next.terminalizeSnapshot(cleanupCtx, sdkterminal.CommandSwallowedAttempt, coreterm.NewAccumulatorSnapshot(nil, false), func(cctx context.Context, _ coreterm.Outcome) error {
			next.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, emptyOperatorUsageShell())
			return nil
		})
		if s.recovery.postOpenLeg != nil {
			s.recovery.postOpenLeg(cleanupCtx, s.facts.billingCallState, s.facts.aLegID, out.bleg, out.cand.Primary, time.Time{}, time.Time{})
		}
		return false, nil
	}
	if attempt.finalStreamObs != nil {
		attempt.finalStreamObs.Finish(ctx, response.OutcomeReplaced)
	}
	// The retryRecvStream is reused for each B-leg. Evidence and terminal
	// finalization state are attempt-scoped and must not leak into the replacement
	// B-leg; the per-leg evidence guard naturally permits the new leg.
	s.responsePipeline.resetForReplacement()
	s.consumeBackendUsageEvidenceForAttempt(ctx, next, out.stream)
	if s.recovery != nil {
		s.recovery.resetPolicy(s.responsePipeline.nowTime)
	}
	views, viewsOK := s.viewsFor(ctx)
	if err := s.responsePipeline.openFinalStreamObservation(ctx, s.facts, next, views, viewsOK, s.terminal.committed()); err != nil && !s.terminal.committed() {
		return false, err
	}
	return true, nil
}
