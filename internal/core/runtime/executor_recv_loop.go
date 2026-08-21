package runtime

// Recv-phase inner-loop control for retryRecvStream. Stream lifecycle
// helpers (Close, handleRecvSuccess, handleRecvEOF,
// etc.) remain in executor_retry_stream.go; this file owns the inner-loop
// state machine that drives per-recv failover within an attempt's budget.

import (
	"context"
	"errors"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// errGateContinueInner signals Recv to pull another inner event without returning to the client yet.
var errGateContinueInner = errors.New("runtime: completion gate continue buffering")

func cancellationAttemptReason(ctx context.Context, recvErr error) string {
	if recvErr != nil {
		if errors.Is(recvErr, context.Canceled) {
			return "context canceled"
		}
		if errors.Is(recvErr, context.DeadlineExceeded) {
			return "context deadline exceeded"
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return "context canceled"
		}
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return "context deadline exceeded"
		}
		return "context done"
	}
	return "cancelled"
}

func (s *retryRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s == nil {
		return lipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	facts := s.facts
	slot := &s.attempt
	p := s.responsePipeline
	terminal := s.terminal
	recovery := s.recovery
	dispatchClientFacingEvent := func(ev lipapi.Event, prepared recvEventPreparation) (lipapi.Event, bool, error) {
		attempt := slot.require()
		transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
		if transformed.err != nil {
			// A tool-policy decision is a surfaced attempt failure. Preserve the
			// historical attempt-lineage record here, while hook/reactor failures
			// continue through terminal settlement without being misclassified as
			// policy evidence.
			if lipapi.IsPolicyDecisionError(transformed.err) {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand,
					Outcome: lipapi.AttemptSurfacedFailure, Reason: attemptReasonDetail(transformed.err), DetailErr: transformed.err,
				}, facts.attemptDiagAttrs(attempt))
			}
			terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, transformed.err)
			return lipapi.Event{}, false, transformed.err
		}
		if transformed.swallowed {
			if transformed.sourceFinished {
				p.forgetToolClassification(transformed.sourceID)
			}
			return lipapi.Event{}, true, nil
		}
		ev = transformed.event
		if len(transformed.gates) > 0 {
			gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
			if errors.Is(gated.err, errGateContinueInner) {
				return lipapi.Event{}, true, nil
			}
			if gated.err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, gated.recording.mandatory(), gated.err)
				return lipapi.Event{}, false, gated.err
			}
			ev = gated.event
			if gated.finishPreflight {
				usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p)
				if err != nil {
					return lipapi.Event{}, false, err
				}
				if ok {
					p.prependRecoveryDrain(ev)
					emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
					return emitted, false, emitErr
				}
			}
			if lipapi.OutputCommitted(ev) {
				terminal.markCommitted(slot.snapshot())
			}
			if ev.Kind == lipapi.EventResponseFinished {
				if p != nil {
					attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
				}
				terminal.finishResponse(p, attempt)
			}
			attempt.accounting.observeClientEvent(p.nowTime(), ev)
			if recovery != nil && recovery.recoverPolicy != nil {
				recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
			}
			if gated.finishPreflight {
				out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
				if err != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
					return lipapi.Event{}, false, err
				}
				if out.Kind == lipapi.EventResponseFinished {
					p.commitSuccessfulTurn(facts, attempt, terminal.committed())
				}
				return out, false, nil
			}
			out, recording, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
			if err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err)
				return lipapi.Event{}, false, err
			}
			return out, false, nil
		}
		attempt.accounting.observeClientEvent(p.nowTime(), ev)
		if recovery != nil && recovery.recoverPolicy != nil {
			recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
		}
		if ev.Kind == lipapi.EventResponseFinished {
			recording := p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
			if recording.mandatory() {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
				return lipapi.Event{}, false, recording.err
			}
			usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p)
			if err != nil {
				if !terminal.finished() {
					terminal.finishResponse(p, attempt)
				}
				return lipapi.Event{}, false, err
			}
			if ok {
				p.rememberClientEvent(ev)
				p.prependRecoveryDrain(ev)
				emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
				return emitted, false, emitErr
			}
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
			p.commitSuccessfulTurn(facts, attempt, terminal.committed())
			terminal.finishResponse(p, attempt)
			terminal.endALeg(aLegEndBase)
			out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
			if err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
				return lipapi.Event{}, false, err
			}
			return out, false, nil
		}
		out, recording, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
		if err != nil {
			terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err)
			return lipapi.Event{}, false, err
		}
		if lipapi.OutputCommitted(out) {
			terminal.markOutputCommittedForAttempt(out, attempt, recovery)
		}
		return out, false, nil
	}
	handleEOF := func() (lipapi.Event, error) {
		attempt := slot.require()
		clearAttemptToolState(p, attempt)
		if gates := p.completionGatesFromContext(ctx); len(gates) > 0 {
			p.abandonIncompleteGateBuffer()
		}
		if recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.eofRecvDecision(p.nowTime())
			if dec.finish {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess, Reason: dec.reason, DetailErr: io.EOF}, facts.attemptDiagAttrs(attempt))
				if dec.warning.Kind != "" {
					p.appendRecoveryDrain(dec.warning)
				}
				p.appendRecoveryDrain(dec.finishEvent)
				head, _ := p.popRecoveryDrain()
				if head.Kind == lipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return lipapi.Event{}, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return lipapi.Event{}, nil
				}
				return out, err
			}
		}
		if !terminal.finished() {
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSurfacedFailure, Reason: "stream ended without response_finished", DetailErr: io.EOF}, facts.attemptDiagAttrs(attempt))
		}
		terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p)
		if !terminal.finished() {
			terminal.finishResponse(p, attempt)
		}
		terminal.endALeg(aLegEndBase)
		return lipapi.Event{}, io.EOF
	}
	handleError := func(recvCtx context.Context, recvErr error, idleDeadline idleContextDeadline, ttftDeadline ttftContextDeadline) (lipapi.Event, bool, error) {
		attempt := slot.require()
		clearAttemptToolState(p, attempt)
		if idleDeadline.expired(recvCtx, recvErr) && recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.idleRecvDecision(p.nowTime())
			if dec.finish {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess, Reason: dec.reason, DetailErr: context.DeadlineExceeded}, facts.attemptDiagAttrs(attempt))
				attempt.cancelAndClose(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: dec.reason}, p.log)
				if dec.warning.Kind != "" {
					p.appendRecoveryDrain(dec.warning)
				}
				p.appendRecoveryDrain(dec.finishEvent)
				out, _ := p.popRecoveryDrain()
				if out.Kind == lipapi.EventResponseFinished {
					p.prependRecoveryDrain(out)
					return lipapi.Event{}, false, nil
				}
				out, cont, emitErr := dispatchClientFacingEvent(out, recvEventPreparation{event: out})
				if cont {
					return lipapi.Event{}, false, nil
				}
				return out, false, emitErr
			}
			if dec.recover {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSwallowedFailure, Reason: dec.reason, DetailErr: dec.err}, facts.attemptDiagAttrs(attempt))
				attempt.cancelAndClose(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: dec.reason}, p.log)
				recovery.exclude(attempt.cand.Key)
				return lipapi.Event{}, true, nil
			}
		}
		if ttftDeadline.expired(recvCtx, recvErr) && !terminal.committed() {
			ttftScope := ttftDeadline.scope
			if ttftScope == ttftTimeoutLeaf {
				tf := ttftFailure(ttftScope, attempt.cand.Key)
				attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSwallowedFailure, Reason: ttftAttemptReason(ttftScope), DetailErr: tf}, facts.attemptDiagAttrs(attempt))
				attempt.cancelAndClose(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "leaf_ttft_timeout"}, p.log)
				recovery.exclude(attempt.cand.Key)
				return lipapi.Event{}, true, nil
			}
			tf := ttftFailure(ttftScope, attempt.cand.Key)
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSurfacedFailure, Reason: ttftAttemptReason(ttftScope), DetailErr: tf}, facts.attemptDiagAttrs(attempt))
			attempt.cancelAndClose(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "global_ttft_timeout"}, p.log)
			terminal.terminalizeTimeout(ctx, facts.terminalFacts(), attempt, p)
			terminal.endALeg(aLegEndBase)
			return lipapi.Event{}, false, lipapi.ErrTTFTTimeout
		}
		if errors.Is(recvErr, context.Canceled) || errors.Is(recvErr, context.DeadlineExceeded) || ctx.Err() != nil {
			reason := cancellationAttemptReason(ctx, recvErr)
			if p != nil && p.log != nil && recvErr != nil {
				p.log.DebugContext(ctx, "retry_recv context cancellation", "reason", reason, "recv_error_detail", recvErrorDetail(recvErr))
			}
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptCancelled, Reason: reason, DetailErr: recvErr}, facts.attemptDiagAttrs(attempt))
			if terminal != nil && terminal.hasALeg() {
				if stream := attempt.detachStream(); stream != nil {
					_ = terminal.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
				}
			} else {
				attempt.cancelAndClose(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone}, p.log)
			}
			terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(recvErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded))
			terminal.endALeg(aLegEndBase)
			return lipapi.Event{}, false, recvErr
		}
		if terminal.committed() || !lipapi.IsRecoverablePreOutput(recvErr) {
			surfErr := recvErr
			if terminal.committed() && lipapi.IsRecoverablePreOutput(recvErr) {
				surfErr = &lipapi.UpstreamFailureError{Phase: lipapi.PhasePostOutput, Recoverable: false, Reason: attemptReasonDetail(recvErr), CandidateKey: attempt.cand.Key}
			}
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSurfacedFailure, Reason: attemptReasonDetail(surfErr), DetailErr: surfErr}, facts.attemptDiagAttrs(attempt))
			terminal.terminalizeSurfacedFailure(ctx, facts.terminalFacts(), attempt, p, surfErr, backendReceivePanic(recvErr))
			return lipapi.Event{}, false, surfErr
		}
		facts.logRecoverablePreOutput(ctx, p.log, attempt.cand.Key)
		attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSwallowedFailure, Reason: "recoverable pre-output (recv)", DetailErr: recvErr}, facts.attemptDiagAttrs(attempt))
		terminal.terminalizeSwallowedAttempt(ctx, facts.terminalFacts(), attempt, p)
		attempt.cancelAndClose(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "recoverable_pre_output"}, p.log)
		recovery.exclude(attempt.cand.Key)
		return lipapi.Event{}, true, nil
	}
	if terminal.finished() {
		return lipapi.Event{}, io.EOF
	}
	attempt := slot.require()
	ctx = p.withDecisionEvidence(facts.projectContext(ctx, p.log), terminal)
	if err := ctx.Err(); err != nil {
		if terminal.finished() {
			return lipapi.Event{}, err
		}
		if attempt.hasInner() {
			attempt.drainSidebandEvidence(ctx, facts, p)
			ev, _, herr := handleError(ctx, err, idleContextDeadline{}, ttftContextDeadline{})
			if herr != nil {
				return ev, herr
			}
			return lipapi.Event{}, err
		}
		attempt.releaseSwallowedAuthority(ctx, p)
		reason := cancellationAttemptReason(ctx, err)
		if p != nil {
			attempt.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptCancelled,
				Reason:    reason,
				DetailErr: err,
			}, facts.attemptDiagAttrs(attempt))
		}
		terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(err, context.DeadlineExceeded))
		terminal.endALeg(aLegEndBase)
		return lipapi.Event{}, err
	}
	if ev, hasRecoveryDrain := p.popRecoveryDrain(); hasRecoveryDrain {
		if ev.Kind == lipapi.EventResponseFinished && (terminal == nil || !terminal.accountingFinalized()) {
			recording := p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
			if recording.mandatory() {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
				return lipapi.Event{}, recording.err
			}
			usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p)
			if err != nil {
				if !terminal.finished() {
					terminal.finishResponse(p, attempt)
				}
				return lipapi.Event{}, err
			}
			if ok {
				p.prependRecoveryDrain(ev)
				return terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
			}
		}
		pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
		out, recording, emitErr := p.observeClientFacing(ctx, ev, responseEventInput{
			facts: facts, attempt: attempt, recovery: recovery,
			pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: ev.Kind == lipapi.EventResponseFinished,
			finishBeforeRelease: true,
		})
		if emitErr != nil {
			terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
		}
		if emitErr == nil && ev.Kind == lipapi.EventResponseFinished {
			terminal.finishResponse(p, attempt)
			terminal.endALeg(aLegEndBase)
		}
		if emitErr == nil && lipapi.OutputCommitted(out) {
			terminal.markOutputCommittedForAttempt(out, attempt, recovery)
		}
		return out, emitErr
	}
	for {
		// Replacement installs a new attempt session while this Recv call
		// continues. Refresh the short-lived snapshot before any attempt-local
		// receive or terminal decision; never carry the retired B-leg identity
		// into the replacement.
		attempt = slot.require()
		if attempt.toolFinal != nil {
			if ev, ok := attempt.toolFinal.popDrain(); ok {
				out, cont, err := dispatchClientFacingEvent(ev, recvEventPreparation{event: ev})
				if cont {
					continue
				}
				return out, err
			}
		}
		if ev, ok := p.popGateDrainHead(); ok {
			// A gate-drain finish is finalized through the same centralized chokepoint as the other
			// response_finished completion paths, before emitGateDrained marks the stream finished, so
			// a reconstructed-usage (ok) result can re-queue the finish and emit the synthesized
			// usage_delta without stranding the finish behind a finished stream. The non-ok result
			// falls through to emitGateDrained + the standard client-event emit. Without this the
			// gate-drain site leaked its reserved authority (it had no finalization at all before
			// centralization).
			if ev.Kind == lipapi.EventResponseFinished && (terminal == nil || !terminal.accountingFinalized()) {
				recording := p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
				if recording.mandatory() {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
					return lipapi.Event{}, recording.err
				}
				usageEv, usageOk, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p)
				if err != nil {
					if !terminal.finished() {
						terminal.finishResponse(p, attempt)
					}
					return lipapi.Event{}, err
				}
				if usageOk {
					p.prependRecoveryDrain(ev)
					emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
					return emitted, emitErr
				}
			}
			if lipapi.OutputCommitted(ev) {
				terminal.markCommitted(slot.snapshot())
			}
			if ev.Kind == lipapi.EventResponseFinished {
				if p != nil {
					attempt.recordAttemptLogged(ctx, recordAttemptParams{
						ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess,
					}, facts.attemptDiagAttrs(attempt))
				}
				terminal.finishResponse(p, attempt)
			}
			attempt.accounting.observeClientEvent(p.nowTime(), ev)
			pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
			out, recording, emitErr := p.observeClientFacing(ctx, ev, responseEventInput{
				facts: facts, attempt: attempt, recovery: recovery,
				pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: ev.Kind == lipapi.EventResponseFinished,
				finishBeforeRelease: true,
			})
			if emitErr != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
			}
			if emitErr == nil && lipapi.OutputCommitted(out) {
				terminal.markOutputCommittedForAttempt(out, attempt, recovery)
			}
			return out, emitErr
		}
		for {
			attempt = slot.require()
			if attempt.hasInner() {
				break
			}
			if slot.publicationIsClosed() {
				if err := terminal.aLegErr(); err != nil {
					return lipapi.Event{}, err
				}
				return lipapi.Event{}, io.EOF
			}
			if terminal.committed() && p.recordingBlocksReplacement() && p.secureRecordingMandatory {
				if err := terminal.terminalizeGateReplacement(ctx, facts.terminalFacts(), slot.require(), p); err != nil {
					return lipapi.Event{}, err
				}
			}
			plan, err := recovery.tryReplacementIteration(ctx, facts.terminalFacts(), attempt, terminal.committed())
			if err != nil {
				// tryReplacementIteration releases the prior (swallowed) attempt's
				// authority reservation before opening the replacement, so on most
				// error paths it is already settled. The early-return guards
				// (ctx.Err, aScope.Err, secure-recording hard stop) return before
				// that release, so release it here when it has not already been
				// settled, then tear down the stream like the other terminal recv exits.
				terminal.releaseSwallowedAttempt(ctx, facts.terminalFacts(), attempt, p)
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p)
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}
			if !plan.opened {
				return p.keepaliveEvent(), nil
			}
			next := plan.next
			if err := terminal.registerReplacement(ctx, plan.open, next); err != nil {
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p)
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}
			clearAttemptToolState(p, attempt)

			// --- TASK 2.2 readiness check BEFORE swapIfOpen ---
			ready, err := recovery.e.prepareReadyAttempt(ctx, next, facts, p, terminal.committed(), plan.open.interleaved, nil)
			if err != nil {
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p)
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}

			old, published := slot.swapIfOpen(ready)
			if !published {
				// Disposal of unconsumed ready attempt must invoke complete attempt terminalization
				ready.Dispose(ctx, errors.New("publication closed"))
				return p.keepaliveEvent(), nil
			}

			// Terminalize the prior attempt with replacement semantics
			evidence := attemptEvidence{
				Command:     sdkterminal.CommandSwallowedAttempt,
				ReleaseKind: authorityapp.ReleaseKindSwallowed,
				LegOutcome:  billing.LegOutcomeSwallowed,
				Usage:       p.operatorUsageForFinalize(),
				TraceID:     facts.traceID,
				ALegID:      facts.aLegID,
			}
			_ = old.TerminalizeAttempt(ctx, IntentReplacement, evidence)

			p.resetForReplacement()
			recovery.resetPolicy(p.nowTime)
		}
		attempt = slot.require()
		// Connector sideband frames can arrive after Open returns. Drain immediately
		// before each receive so pre-first-event evidence is accounted even when the
		// transport reports its first read error or cancellation.
		attempt.drainSidebandEvidence(ctx, facts, p)
		recvCtx := ctx
		var cancelRecv context.CancelFunc = func() {}
		ttftDeadline := ttftContextDeadline{}
		if !terminal.committed() && recovery != nil && recovery.ttft != nil {
			recvCtx, cancelRecv, ttftDeadline = recovery.ttft.scopedContext(ctx, p.nowTime(), attempt.cand.Key, attempt.cand.Primary.TTFTTimeout)
		}
		recvCtx, cancelRecv, idleDeadline := recovery.scopedIdleContext(recvCtx, cancelRecv, p.nowTime())
		ev, err := attempt.receive(recvCtx, terminal.committed())
		cancelRecv()
		// Evidence may be published during the receive itself. Drain after the
		// call so a final event, EOF, or error cannot discard that evidence.
		attempt.drainSidebandEvidence(ctx, facts, p)
		// Close/cancel may have terminalized while we were blocked. Do not run
		// NormalFinish (or surface bare context.Canceled) after that owner won.
		if terminal.finished() {
			if terminal != nil && terminal.hasALeg() {
				if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
					return lipapi.Event{}, scopeErr
				}
			}
			return lipapi.Event{}, io.EOF
		}
		if err != nil && terminal != nil && terminal.hasALeg() {
			if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{
					ALegID:    facts.aLegID,
					BLeg:      attempt.bleg,
					Cand:      attempt.cand,
					Outcome:   lipapi.AttemptCancelled,
					Reason:    "a-leg canceled",
					DetailErr: scopeErr,
				}, facts.attemptDiagAttrs(attempt))
				_ = attempt.detachStream()
				terminal.terminalizeALegCancellation(ctx, facts.terminalFacts(), attempt, p, "a-leg canceled")
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, scopeErr
			}
		}
		if err == nil {
			prepared := p.prepareRecvEvent(ctx, facts, attempt, ev)
			if prepared.err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, prepared.err)
				return lipapi.Event{}, prepared.err
			}
			if prepared.swallowed {
				continue
			}
			ev, cont, err := dispatchClientFacingEvent(ev, prepared)
			if cont {
				continue
			}
			return ev, err
		}
		if errors.Is(err, io.EOF) {
			return handleEOF()
		}
		ev, cont, err := handleError(recvCtx, err, idleDeadline, ttftDeadline)
		if cont {
			continue
		}
		return ev, err
	}
}
