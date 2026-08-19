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

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
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
	if s.isFinished() {
		return lipapi.Event{}, io.EOF
	}
	attempt := s.attempt.require()
	ctx = s.recvExecContext(ctx)
	if err := ctx.Err(); err != nil {
		if s.isFinished() {
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
			attempt.authority.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindSwallowed, s.operatorUsageForFinalize())
		}
		reason := cancellationAttemptReason(ctx, err)
		if s.executor != nil {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
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
		s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
			s.persistCancellationBilling(cctx, attempt, reason)
			s.finishFinalStreamObservation(cctx, response.OutcomeCancelled)
			s.markFinished()
			return nil
		}, func(cctx context.Context, _ coreterm.Outcome) error {
			s.recordBillingLegForAttempt(cctx, attempt, cmd)
			s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, cmd)
			return nil
		})
		if !s.isFinished() {
			s.markFinished()
		}
		s.terminal.endALeg(aLegEndBase)
		return lipapi.Event{}, err
	}
	if len(s.recoverDrain) > 0 {
		ev := s.recoverDrain[0]
		s.recoverDrain = s.recoverDrain[1:]
		if ev.Kind == lipapi.EventResponseFinished && (s.terminal == nil || !s.terminal.accountingFinalized()) {
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
			s.terminal.endALeg(aLegEndBase)
		}
		pm, _ := s.recvHookMeta()
		return s.emitClientFacingObserved(ctx, ev, pm)
	}
	for {
		// Replacement installs a new attempt session while this Recv call
		// continues. Refresh the short-lived snapshot before any attempt-local
		// receive or terminal decision; never carry the retired B-leg identity
		// into the replacement.
		attempt = s.attempt.require()
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
			if ev.Kind == lipapi.EventResponseFinished && (s.terminal == nil || !s.terminal.accountingFinalized()) {
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
			attempt.accounting.observeClientEvent(s.now(), ev)
			pm, _ := s.recvHookMeta()
			return s.emitClientFacingObserved(ctx, ev, pm)
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
					attempt.terminalizeSnapshot(ctx, sdkterminal.CommandSwallowedAttempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
						attempt.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, s.operatorUsageForFinalize())
						s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandSwallowedAttempt)
						return nil
					})
				}
				s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandPartialError, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
					s.finishFinalStreamObservation(cctx, response.OutcomeFailed)
					s.markFinished()
					return nil
				}, func(cctx context.Context, _ coreterm.Outcome) error {
					s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandPartialError)
					s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandPartialError)
					return nil
				})
				if !s.isFinished() {
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
		if !s.isCommitted() && s.ttft != nil {
			recvCtx, cancelRecv, ttftDeadline = s.ttft.scopedContext(ctx, s.now(), attempt.cand.Key, attempt.cand.Primary.TTFTTimeout)
		}
		recvCtx, cancelRecv, idleDeadline := s.scopedIdleContext(recvCtx, cancelRecv, s.now())
		ev, err := safety.CallValue(safety.BoundaryBackend, "backend_recv", func() (lipapi.Event, error) {
			return inner.Recv(recvCtx)
		})
		cancelRecv()
		// Evidence may be published during the receive itself. Drain after the
		// call so a final event, EOF, or error cannot discard that evidence.
		s.consumeBackendUsageEvidenceForAttempt(ctx, attempt, inner)
		// Close/cancel may have terminalized while we were blocked. Do not run
		// NormalFinish (or surface bare context.Canceled) after that owner won.
		if s.isFinished() {
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
				err = mapStreamPanic(pe, s.isCommitted())
			}
		}
		if err != nil && s.terminal != nil && s.terminal.hasALeg() {
			if scopeErr := s.terminal.aLegErr(); errors.Is(scopeErr, leglifecycle.ErrALegCanceled) {
				s.executor.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    s.facts.aLegID,
					BLeg:      attempt.bleg,
					Cand:      attempt.cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "a-leg canceled",
					DetailErr: scopeErr,
				}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
				_ = attempt.takeInner()
				s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandCancel, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
					s.persistCancellationBilling(cctx, attempt, "a-leg canceled")
					s.finishFinalStreamObservation(cctx, response.OutcomeCancelled)
					s.markFinished()
					return nil
				}, func(cctx context.Context, _ coreterm.Outcome) error {
					s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandCancel)
					s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandCancel)
					return nil
				})
				if !s.isFinished() {
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
	if s.isCommitted() && s.secureRecvRecordingHardStop && s.executor != nil && s.executor.SecureSessionRecordingMandatory {
		// Output committed: compete for gate-replacement rejection evidence (D13) without effects.
		_ = s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandGateReplacement, attempt, s.accumulatorSnapshot(), nil, func(cctx context.Context, _ coreterm.Outcome) error {
			s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandGateReplacement)
			s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandGateReplacement)
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
			attempt.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, s.operatorUsageForFinalize())
			return nil
		}
		// Recoverable recv errors may already have consumed the attempt terminal
		// while recording the swallowed attempt. The terminal is never reset;
		// finish authority directly in that case. Direct replacement callers still
		// claim the fresh attempt terminal before applying the same effects.
		if owner := attempt.terminal.Owner(); owner != nil && owner.State() == sdkterminal.StateOpen {
			attempt.terminalizeSnapshot(ctx, sdkterminal.CommandSwallowedAttempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
				err := finalize(cctx)
				s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandSwallowedAttempt)
				return err
			})
		} else {
			_ = finalize(ctx)
		}
	}
	out, err := s.executor.tryPlanOpenOnce(ctx, attemptOpenParams{
		bus:                      s.bus,
		traceID:                  s.facts.traceID,
		aLegID:                   s.facts.aLegID,
		aScope:                   s.terminal.aLegScope(),
		baseline:                 s.facts.baseline,
		failoverReq:              capabilities.NewFailoverRequirementSet(s.facts.baseline),
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
		billingCallID:            s.facts.billingCallID,
		billingCallState:         s.facts.billingCallState,
	})
	if err != nil {
		return false, err
	}
	if !out.opened {
		s.interleaved = out.interleaved
		return false, nil
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
			l := s.executor.newAttemptAuthorityLifecycle(out.authority, out.cand)
			_ = terminalizeAttemptEphemeral(ctx, sdkterminal.CommandSwallowedAttempt, false, func(cctx context.Context) error {
				l.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindSwallowed, emptyOperatorUsageShell())
				return nil
			})
			s.executor.appendPostOpenTerminalLeg(ctx, s.facts.billingCallState, s.facts.aLegID, out.bleg, out.cand.Primary, time.Time{}, time.Time{})
			return false, err
		}
	}
	fs, maxArgs := s.executor.resolveToolCallFinalizers()
	next := newAttemptSession(attemptSessionInput{
		inner:                 out.stream,
		bleg:                  out.bleg,
		cand:                  out.cand,
		authority:             s.executor.newAttemptAuthorityLifecycle(out.authority, out.cand),
		accounting:            newAttemptAccountingTracker(s.now()),
		toolFinal:             newToolCallAssembler(fs, maxArgs, s.facts.baseline.Tools),
		promptCacheSource:     promptCacheObservationSource(out.stream),
		promptCacheController: promptCacheControllerFor(s.executor.Backends[out.cand.Primary.Backend]),
		finalStreamObs:        &extensions.FinalStreamObservationSession{Log: s.executor.Log, Metrics: s.executor.ExtensionMetrics},
	})
	s.resetToolFinal()
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
		s.executor.appendPostOpenTerminalLeg(cleanupCtx, s.facts.billingCallState, s.facts.aLegID, out.bleg, out.cand.Primary, time.Time{}, time.Time{})
		return false, nil
	}
	if attempt.finalStreamObs != nil {
		attempt.finalStreamObs.Finish(ctx, response.OutcomeReplaced)
	}
	s.interleaved = out.interleaved
	s.clearClientAccumulators()
	if s.customer != nil {
		s.customer.resetContent()
	}
	// The retryRecvStream is reused for each B-leg. Evidence and terminal
	// finalization state are attempt-scoped and must not leak into the replacement
	// B-leg; the per-leg evidence guard naturally permits the new leg.
	s.lastAuthorityUsage = lipapi.Event{}
	s.lastCustomerUsage = lipapi.Event{}
	s.consumeBackendUsageEvidenceForAttempt(ctx, next, out.stream)
	if s.executor != nil {
		s.recoverPolicy = streamrecovery.NewPolicy(s.executor.StreamRecovery, s.now())
	}
	if err := s.openFinalStreamObservation(ctx); err != nil && !s.isCommitted() {
		return false, err
	}
	return true, nil
}
