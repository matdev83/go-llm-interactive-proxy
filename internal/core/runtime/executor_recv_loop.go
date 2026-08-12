package runtime

// Recv-phase inner-loop control for retryRecvStream. Stream lifecycle
// helpers (loadInner, storeInner, Close, handleRecvSuccess, handleRecvEOF,
// etc.) remain in executor_retry_stream.go; this file owns the inner-loop
// state machine that drives per-recv failover within an attempt's budget.

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
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
	if s.isFinished() {
		return lipapi.Event{}, io.EOF
	}
	ctx = s.recvExecContext(ctx)
	if err := ctx.Err(); err != nil {
		if s.isFinished() {
			return lipapi.Event{}, err
		}
		if inner := s.loadInner(); inner != nil {
			s.consumeBackendUsageEvidence(ctx, inner)
			ev, _, herr := s.handleRecvError(ctx, ctx, err, idleContextDeadline{}, ttftContextDeadline{})
			if herr != nil {
				return ev, herr
			}
			return lipapi.Event{}, err
		}
		if !s.authority.Settled() {
			s.authority.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindSwallowed, s.operatorUsageForFinalize())
		}
		reason := cancellationAttemptReason(ctx, err)
		if s.executor != nil {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptCancelled,
				Reason:    reason,
				DetailErr: err,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		}
		cmd := sdkterminal.CommandCancel
		if errors.Is(err, context.DeadlineExceeded) {
			cmd = sdkterminal.CommandTimeout
		}
		s.runStreamTerminal(ctx, cmd, func(cctx context.Context) error {
			s.persistCancellationBilling(cctx, reason)
			s.finishFinalStreamObservation(cctx, response.OutcomeCancelled)
			s.markFinished()
			return nil
		})
		if !s.isFinished() {
			s.markFinished()
		}
		s.finishALegScope()
		return lipapi.Event{}, err
	}
	if len(s.recoverDrain) > 0 {
		ev := s.recoverDrain[0]
		s.recoverDrain = s.recoverDrain[1:]
		if ev.Kind == lipapi.EventResponseFinished && !s.tokenAccountingFinalized {
			if err := s.mandatoryClientFacingPreflight(ctx, ev); err != nil {
				return lipapi.Event{}, err
			}
			usageEv, ok, err := s.finalizeResponseFinishedAuthority(ctx, ev)
			if err != nil {
				if !s.isFinished() {
					s.markFinished()
				}
				return lipapi.Event{}, err
			}
			if ok {
				s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
				return s.emitSynthesizedUsage(ctx, usageEv)
			}
		}
		if ev.Kind == lipapi.EventResponseFinished {
			s.markFinished()
			s.finishALegScope()
		}
		pm, _ := s.recvHookMeta()
		return s.emitClientFacingObserved(ctx, ev, pm)
	}
	for {
		if ev, ok := s.popToolFinalDrain(); ok {
			out, cont, err := s.dispatchClientFacingEvent(ctx, ev)
			if cont {
				continue
			}
			return out, err
		}
		if ev, ok := s.popGateDrainHead(); ok {
			// A gate-drain finish is finalized through the same centralized chokepoint as the other
			// response_finished completion paths, before emitGateDrained marks the stream finished, so
			// a reconstructed-usage (ok) result can re-queue the finish and emit the synthesized
			// usage_delta without stranding the finish behind a finished stream. The non-ok result
			// falls through to emitGateDrained + the standard client-event emit. Without this the
			// gate-drain site leaked its reserved authority (it had no finalization at all before
			// centralization).
			if ev.Kind == lipapi.EventResponseFinished && !s.tokenAccountingFinalized {
				if err := s.mandatoryClientFacingPreflight(ctx, ev); err != nil {
					return lipapi.Event{}, err
				}
				usageEv, usageOk, err := s.finalizeResponseFinishedAuthority(ctx, ev)
				if err != nil {
					if !s.isFinished() {
						s.markFinished()
					}
					return lipapi.Event{}, err
				}
				if usageOk {
					s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
					emitted, emitErr := s.emitSynthesizedUsage(ctx, usageEv)
					return emitted, emitErr
				}
			}
			ev = s.emitGateDrained(ctx, ev)
			s.accounting.observeClientEvent(s.now(), ev)
			pm, _ := s.recvHookMeta()
			return s.emitClientFacingObserved(ctx, ev, pm)
		}
		var inner lipapi.ManagedEventStream
		for {
			inner = s.loadInner()
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
				if !s.authority.Settled() {
					s.runAttemptTerminal(ctx, sdkterminal.CommandSwallowedAttempt, func(cctx context.Context) error {
						s.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, s.operatorUsageForFinalize())
						return nil
					})
					s.resetAttemptTerminal()
				}
				s.runStreamTerminal(ctx, sdkterminal.CommandPartialError, func(cctx context.Context) error {
					s.finishFinalStreamObservation(cctx, response.OutcomeFailed)
					s.markFinished()
					return nil
				})
				if !s.isFinished() {
					s.markFinished()
				}
				s.finishALegScope()
				return lipapi.Event{}, err
			}
			if !opened {
				return stream.DefaultKeepaliveEvent(), nil
			}
		}
		// Connector sideband frames can arrive after Open returns. Drain immediately
		// before each receive so pre-first-event evidence is accounted even when the
		// transport reports its first read error or cancellation.
		s.consumeBackendUsageEvidence(ctx, inner)
		recvCtx := ctx
		var cancelRecv context.CancelFunc = func() {}
		ttftDeadline := ttftContextDeadline{}
		if !s.isCommitted() && s.ttft != nil {
			recvCtx, cancelRecv, ttftDeadline = s.ttft.scopedContext(ctx, s.now(), s.cand.Key, s.cand.Primary.TTFTTimeout)
		}
		recvCtx, cancelRecv, idleDeadline := s.scopedIdleContext(recvCtx, cancelRecv, s.now())
		ev, err := safety.CallValue(safety.BoundaryBackend, "backend_recv", func() (lipapi.Event, error) {
			return inner.Recv(recvCtx)
		})
		cancelRecv()
		// Evidence may be published during the receive itself. Drain after the
		// call so a final event, EOF, or error cannot discard that evidence.
		s.consumeBackendUsageEvidence(ctx, inner)
		// Close/cancel may have terminalized while we were blocked. Do not run
		// NormalFinish (or surface bare context.Canceled) after that owner won.
		if s.isFinished() {
			if s.aScope != nil {
				if scopeErr := s.aScope.Err(); errors.Is(scopeErr, leglifecycle.ErrALegCanceled) {
					return lipapi.Event{}, scopeErr
				}
			}
			return lipapi.Event{}, io.EOF
		}
		if err != nil {
			var pe *safety.PanicError
			if errors.As(err, &pe) {
				err = mapStreamPanic(pe, s.isCommitted())
			}
		}
		if err != nil && s.aScope != nil {
			if scopeErr := s.aScope.Err(); errors.Is(scopeErr, leglifecycle.ErrALegCanceled) {
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.aLegID,
					BLeg:      s.bleg,
					Cand:      s.cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "a-leg canceled",
					DetailErr: scopeErr,
				}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
				_ = s.takeAndNilInner()
				s.runStreamTerminal(ctx, sdkterminal.CommandCancel, func(cctx context.Context) error {
					s.persistCancellationBilling(cctx, "a-leg canceled")
					s.finishFinalStreamObservation(cctx, response.OutcomeCancelled)
					s.markFinished()
					return nil
				})
				if !s.isFinished() {
					s.markFinished()
				}
				s.finishALegScope()
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
// It returns opened=true when s.inner is ready, opened=false when the caller should emit
// a keepalive (Req 5.5) and invoke Recv again, or a non-nil error when the replacement path is exhausted.
func (s *retryRecvStream) tryReplacementIteration(ctx context.Context) (opened bool, err error) {
	ctx = diag.EnsureCallDiag(ctx, s.traceID, s.aLegID)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.isCommitted() && s.secureRecvRecordingHardStop && s.executor != nil && s.executor.SecureSessionRecordingMandatory {
		// Output committed: compete for gate-replacement rejection evidence (D13) without effects.
		_ = s.runStreamTerminal(ctx, sdkterminal.CommandGateReplacement, nil)
		return false, &lipapi.UpstreamFailureError{
			Phase:        lipapi.PhasePostOutput,
			Recoverable:  false,
			Reason:       "secure session mandatory recorder failure after committed output",
			CandidateKey: strings.TrimSpace(s.cand.Key),
		}
	}
	if s.aScope != nil {
		if err := s.aScope.Err(); err != nil {
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
	if !s.authority.Settled() {
		s.runAttemptTerminal(ctx, sdkterminal.CommandSwallowedAttempt, func(cctx context.Context) error {
			s.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, s.operatorUsageForFinalize())
			return nil
		})
		s.resetAttemptTerminal()
	}
	out, err := s.executor.tryPlanOpenOnce(ctx, attemptOpenParams{
		bus:                      s.bus,
		traceID:                  s.traceID,
		aLegID:                   s.aLegID,
		aScope:                   s.aScope,
		baseline:                 s.baseline,
		failoverReq:              capabilities.NewFailoverRequirementSet(s.baseline),
		sel:                      s.sel,
		requestSize:              s.requestSize,
		session:                  s.session,
		excluded:                 s.excluded,
		rng:                      s.rng,
		budget:                   s.budget,
		ttft:                     s.ttft,
		isRetryPath:              true,
		lastReject:               &s.lastHardReject,
		lastTransportReject:      &s.lastHardTransportReject,
		lastAdmissionErr:         &s.lastAdmissionErr,
		affinityKey:              s.affinityKey,
		affinitySet:              s.affinitySet,
		isContextLimitExhaustion: &s.isContextLimitExhaustion,
		transformExcludes:        &s.transformExcludes,
		interleaved:              s.interleaved,
		suppressThinker:          s.suppressThinker,
		suppressVisibleMemo:      s.suppressVisibleMemo,
		lastParallelFailure:      &s.lastParallelFailure,
	})
	if err != nil {
		return false, err
	}
	if !out.opened {
		s.interleaved = out.interleaved
		return false, nil
	}
	s.interleaved = out.interleaved
	if s.aScope != nil && !out.registered {
		if err := s.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
			ID:      out.bleg.BLegID,
			Attempt: lifecycleAttempt(out.stream),
		}); err != nil {
			if out.stream != nil && !errors.Is(err, leglifecycle.ErrALegCanceled) {
				_ = out.stream.Close()
			}
			// out.authority was freshly admitted for this replacement attempt and
			// is not yet assigned to s.authority (that happens only on the success
			// path below), so release it here to avoid leaking the reservation. The
			// prior swallowed s.authority was already released before tryPlanOpenOnce.
			l := s.executor.newAttemptAuthorityLifecycle(out.authority, out.cand)
			_ = terminalizeAttemptEphemeral(ctx, sdkterminal.CommandSwallowedAttempt, false, func(cctx context.Context) error {
				l.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, emptyOperatorUsageShell())
				return nil
			})
			return false, err
		}
	}
	s.finishFinalStreamObservation(ctx, response.OutcomeReplaced)
	s.storeInner(out.stream)
	s.bleg = out.bleg
	s.cand = out.cand
	s.clearClientAccumulators()
	if s.customer != nil {
		s.customer.resetContent()
	}
	s.tokenAccountingFinalized = false
	s.accounting = newAttemptAccountingTracker(s.now())
	s.consumeBackendUsageEvidence(ctx, out.stream)
	s.resetToolFinal()
	if s.executor != nil {
		s.recoverPolicy = streamrecovery.NewPolicy(s.executor.StreamRecovery, s.now())
	}
	s.authority.Reset(out.authority, out.cand)
	if err := s.openFinalStreamObservation(ctx); err != nil && !s.isCommitted() {
		return false, err
	}
	return true, nil
}
