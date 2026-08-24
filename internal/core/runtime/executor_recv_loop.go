package runtime

// Recv-phase inner-loop control for retryRecvStream. Stream lifecycle
// helpers (Close, handleRecvSuccess, handleRecvEOF,
// etc.) remain in executor_retry_stream.go; this file owns the inner-loop
// state machine that drives per-recv failover within an attempt's budget.

import (
	"context"
	"errors"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
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
				if terminal.isLoopGuardEnabled() {
					outcome := terminal.agentLoopGuardEvaluate(ctx, facts.terminalFacts(), attempt, p, ev)
					held := outcome.Action == stopguard.ActionContinueLeg && !outcome.HoldReleased
					if held {
						if terminal.tryGuardContinuation(ctx, s, attempt, outcome) {
							return lipapi.Event{}, true, nil
						}
						fallback := terminal.guardHeldFallback(ctx, attempt, p, "dispatch_gated", outcome.Reason)
						attempt.accounting.observeClientEvent(p.nowTime(), fallback)
						if recovery != nil && recovery.recoverPolicy != nil {
							recovery.recoverPolicy.ObserveClientEvent(fallback, p.nowTime())
						}
						out, recording, err := p.observeClientFacing(ctx, fallback, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
						if err != nil {
							terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err)
							return lipapi.Event{}, false, err
						}
						return out, false, nil
					}
					if p != nil {
						attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
					}
					terminal.finishResponse(p, attempt)
				} else {
					if p != nil {
						attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
					}
					terminal.finishResponseGuarded(ctx, facts.terminalFacts(), attempt, p, ev, "dispatch_gated")
				}
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
			if terminal.isLoopGuardEnabled() {
				outcome := terminal.agentLoopGuardEvaluate(ctx, facts.terminalFacts(), attempt, p, ev)
				held := outcome.Action == stopguard.ActionContinueLeg && !outcome.HoldReleased
				if held {
					if terminal.tryGuardContinuation(ctx, s, attempt, outcome) {
						return lipapi.Event{}, true, nil
					}
					if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
						return lipapi.Event{}, false, err
					}
					fallback := terminal.guardHeldFallback(ctx, attempt, p, "dispatch_nongated", outcome.Reason)
					attempt.accounting.observeClientEvent(p.nowTime(), fallback)
					if recovery != nil && recovery.recoverPolicy != nil {
						recovery.recoverPolicy.ObserveClientEvent(fallback, p.nowTime())
					}
					out, _, err := p.observeClientFacing(ctx, fallback, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
					if err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
						return lipapi.Event{}, false, err
					}
					return out, false, nil
				}
				if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
					return lipapi.Event{}, false, err
				}
			}
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
			if terminal.isLoopGuardEnabled() {
				// Not held: settle B-attempt as success via terminal CAS for exactly-once (Req 9.1)
				attempt.TerminalizeAttempt(ctx, IntentSuccess, attemptEvidence{
					RecordOutcome: lipapi.AttemptSuccess,
					RecordReason:  "success",
					TraceID:       attempt.traceID,
					ALegID:        attempt.bleg.ALegID,
					StartedAt:     attempt.accounting.requestStartedAt,
				})
			} else {
				attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
			}
			p.commitSuccessfulTurn(facts, attempt, terminal.committed())
			if terminal.isLoopGuardEnabled() {
				// Already verified not held, so directly finish without second verifier
				terminal.finishResponse(p, attempt)
				terminal.endALeg(aLegEndBase)
			} else {
				terminal.finishResponseGuarded(ctx, facts.terminalFacts(), attempt, p, ev, "dispatch_nongated")
			}
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
	handleEOF := func() (lipapi.Event, bool, error) {
		attempt := slot.require()
		clearAttemptToolState(p, attempt)
		if gates := p.completionGatesFromContext(ctx); len(gates) > 0 {
			p.abandonIncompleteGateBuffer()
		}
		if recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.eofRecvDecision(p.nowTime())
			if dec.finish {
				if dec.warning.Kind != "" {
					p.appendRecoveryDrain(dec.warning)
				}
				p.appendRecoveryDrain(dec.finishEvent)
				head, _ := p.popRecoveryDrain()
				if head.Kind == lipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return lipapi.Event{}, false, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return lipapi.Event{}, false, nil
				}
				return out, false, err
			}
			if dec.recover {
				attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), dec.reason, dec.err)
				recovery.exclude(attempt.cand.Key)
				return lipapi.Event{}, true, nil
			}
			if dec.continuePostOutput {
				if ctx.Err() != nil {
					reason := cancellationAttemptReason(ctx, ctx.Err())
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(ctx.Err(), context.DeadlineExceeded))
					if terminal.hasALeg() {
						_ = terminal.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
					}
					terminal.endALeg(aLegEndBase)
					return lipapi.Event{}, false, ctx.Err()
				}
				if terminal.isLoopGuardEnabled() && s != nil && terminal.tryPostOutputContinuation(ctx, s, attempt, stopguard.CauseTransportEOFPostCommit, dec.reason) {
					return lipapi.Event{}, true, nil
				}
				if terminal.isLoopGuardEnabled() {
					if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
						return lipapi.Event{}, false, err
					}
					terminal.settlePostOutputInterruptedBAttempt(ctx, attempt, stopguard.CauseTransportEOFPostCommit, dec.reason)
					fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: postOutputInterruptionReason}
					pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
					attempt.accounting.observeClientEvent(p.nowTime(), fallback)
					if recovery != nil && recovery.recoverPolicy != nil {
						recovery.recoverPolicy.ObserveClientEvent(fallback, p.nowTime())
					}
					out, _, err := p.observeClientFacing(ctx, fallback, responseEventInput{
						facts: facts, attempt: attempt, recovery: recovery,
						pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishBeforeRelease: true,
					})
					if err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
						return lipapi.Event{}, false, err
					}
					terminal.finishResponse(p, attempt)
					terminal.endALeg(aLegEndBase)
					return out, false, nil
				}
				terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p)
				fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: postOutputInterruptionReason}
				p.appendRecoveryDrain(fallback)
				head, _ := p.popRecoveryDrain()
				if head.Kind == lipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return lipapi.Event{}, false, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return lipapi.Event{}, false, nil
				}
				return out, false, err
			}
		}
		terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p)
		if !terminal.finished() {
			terminal.finishResponse(p, attempt)
		}
		terminal.endALeg(aLegEndBase)
		return lipapi.Event{}, false, io.EOF
	}
	handleError := func(recvCtx context.Context, recvErr error, idleDeadline idleContextDeadline, ttftDeadline ttftContextDeadline) (lipapi.Event, bool, error) {
		attempt := slot.require()
		clearAttemptToolState(p, attempt)
		if idleDeadline.expired(recvCtx, recvErr) && recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.idleRecvDecision(p.nowTime())
			if dec.finish {
				attempt.setPendingCancelCause(lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: dec.reason})
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
				attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), dec.reason, dec.err)
				recovery.exclude(attempt.cand.Key)
				return lipapi.Event{}, true, nil
			}
			if dec.continuePostOutput {
				if ctx.Err() != nil {
					reason := cancellationAttemptReason(ctx, ctx.Err())
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(ctx.Err(), context.DeadlineExceeded))
					if terminal.hasALeg() {
						_ = terminal.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
					}
					terminal.endALeg(aLegEndBase)
					return lipapi.Event{}, false, ctx.Err()
				}
				if terminal.isLoopGuardEnabled() && s != nil && terminal.tryPostOutputContinuation(ctx, s, attempt, stopguard.CauseIdlePostCommit, dec.reason) {
					return lipapi.Event{}, true, nil
				}
				if terminal.isLoopGuardEnabled() {
					if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
						return lipapi.Event{}, false, err
					}
					terminal.settlePostOutputInterruptedBAttempt(ctx, attempt, stopguard.CauseIdlePostCommit, dec.reason)
					fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: postOutputInterruptionReason}
					pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
					attempt.accounting.observeClientEvent(p.nowTime(), fallback)
					if recovery != nil && recovery.recoverPolicy != nil {
						recovery.recoverPolicy.ObserveClientEvent(fallback, p.nowTime())
					}
					out, _, err := p.observeClientFacing(ctx, fallback, responseEventInput{
						facts: facts, attempt: attempt, recovery: recovery,
						pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishBeforeRelease: true,
					})
					if err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
						return lipapi.Event{}, false, err
					}
					terminal.finishResponse(p, attempt)
					terminal.endALeg(aLegEndBase)
					return out, false, nil
				}
				attempt.setPendingCancelCause(lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: dec.reason})
				fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: postOutputInterruptionReason}
				p.appendRecoveryDrain(fallback)
				head, _ := p.popRecoveryDrain()
				if head.Kind == lipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return lipapi.Event{}, false, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return lipapi.Event{}, false, nil
				}
				return out, false, err
			}
		}
		if ttftDeadline.expired(recvCtx, recvErr) && !terminal.committed() {
			ttftScope := ttftDeadline.scope
			if ttftScope == ttftTimeoutLeaf {
				tf := ttftFailure(ttftScope, attempt.cand.Key)
				attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), ttftAttemptReason(ttftScope), tf)
				recovery.exclude(attempt.cand.Key)
				return lipapi.Event{}, true, nil
			}
			terminal.terminalizeTimeout(ctx, facts.terminalFacts(), attempt, p)
			terminal.endALeg(aLegEndBase)
			return lipapi.Event{}, false, lipapi.ErrTTFTTimeout
		}
		if errors.Is(recvErr, context.Canceled) || errors.Is(recvErr, context.DeadlineExceeded) || ctx.Err() != nil {
			reason := cancellationAttemptReason(ctx, recvErr)
			if p != nil && p.log != nil && recvErr != nil {
				p.log.DebugContext(ctx, "retry_recv context cancellation", "reason", reason, "recv_error_detail", recvErrorDetail(recvErr))
			}
			terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(recvErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded))
			if terminal != nil && terminal.hasALeg() {
				_ = terminal.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
			}
			terminal.endALeg(aLegEndBase)
			return lipapi.Event{}, false, recvErr
		}
		if terminal.committed() && terminal.isLoopGuardEnabled() && recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.genericErrorRecvDecision(recvErr, p.nowTime())
			if dec.continuePostOutput {
				if ctx.Err() != nil {
					reason := cancellationAttemptReason(ctx, ctx.Err())
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(ctx.Err(), context.DeadlineExceeded))
					if terminal.hasALeg() {
						_ = terminal.cancelALeg(ctx, lipapi.CancelCause{Kind: lipapi.CancelContextDone})
					}
					terminal.endALeg(aLegEndBase)
					return lipapi.Event{}, false, ctx.Err()
				}
				if s != nil && terminal.tryPostOutputContinuation(ctx, s, attempt, stopguard.CauseTransportEOFPostCommit, dec.reason) {
					return lipapi.Event{}, true, nil
				}
				if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
					return lipapi.Event{}, false, err
				}
				terminal.settlePostOutputInterruptedBAttempt(ctx, attempt, stopguard.CauseTransportEOFPostCommit, dec.reason)
				fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: postOutputInterruptionReason}
				pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
				attempt.accounting.observeClientEvent(p.nowTime(), fallback)
				if recovery != nil && recovery.recoverPolicy != nil {
					recovery.recoverPolicy.ObserveClientEvent(fallback, p.nowTime())
				}
				out, _, err := p.observeClientFacing(ctx, fallback, responseEventInput{
					facts: facts, attempt: attempt, recovery: recovery,
					pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishBeforeRelease: true,
				})
				if err != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
					return lipapi.Event{}, false, err
				}
				terminal.finishResponse(p, attempt)
				terminal.endALeg(aLegEndBase)
				return out, false, nil
			}
		}
		if terminal.committed() || !lipapi.IsRecoverablePreOutput(recvErr) {
			surfErr := recvErr
			if terminal.committed() && lipapi.IsRecoverablePreOutput(recvErr) {
				surfErr = &lipapi.UpstreamFailureError{Phase: lipapi.PhasePostOutput, Recoverable: false, Reason: attemptReasonDetail(recvErr), CandidateKey: attempt.cand.Key}
			}
			terminal.terminalizeSurfacedFailure(ctx, facts.terminalFacts(), attempt, p, surfErr, backendReceivePanic(recvErr))
			return lipapi.Event{}, false, surfErr
		}
		facts.logRecoverablePreOutput(ctx, p.log, attempt.cand.Key)
		attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), "recoverable pre-output (recv)", recvErr)
		recovery.exclude(attempt.cand.Key)
		return lipapi.Event{}, true, nil
	}
	if terminal.finished() || (terminal.isInterleavedThinker() && terminal.accountingFinalized()) {
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
		reason := cancellationAttemptReason(ctx, err)
		attempt.terminalizeEarlyCancellation(ctx, facts, p, terminal.committed(), reason, err)
		terminal.finishResponse(p, attempt)
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
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
				return lipapi.Event{}, err
			}
			if ok {
				p.prependRecoveryDrain(ev)
				return terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
			}
		}
		pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
		// Guard hold check before emitting raw terminal (Req 1.3/12.10)
		if ev.Kind == lipapi.EventResponseFinished && terminal.isLoopGuardEnabled() {
			outcome := terminal.agentLoopGuardEvaluate(ctx, facts.terminalFacts(), attempt, p, ev)
			held := outcome.Action == stopguard.ActionContinueLeg && !outcome.HoldReleased
			if held {
				if terminal.tryGuardContinuation(ctx, s, attempt, outcome) {
					return lipapi.Event{}, nil
				}
				if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
					return lipapi.Event{}, err
				}
				terminal.settleSwallowedBAttempt(ctx, attempt)
				if terminal.log != nil {
					terminal.log.DebugContext(ctx, "agent_loop_guard_hold", "source", "recovery_drain", "reason", boundGuardReason(outcome.Reason+" "+guardContinuationPendingReason))
				}
				fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: guardContinuationPendingReason}
				terminal.finishResponse(p, attempt)
				terminal.endALeg(aLegEndBase)
				attempt.accounting.observeClientEvent(p.nowTime(), fallback)
				if recovery != nil && recovery.recoverPolicy != nil {
					recovery.recoverPolicy.ObserveClientEvent(fallback, p.nowTime())
				}
				out, recording, emitErr := p.observeClientFacing(ctx, fallback, responseEventInput{
					facts: facts, attempt: attempt, recovery: recovery,
					pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishBeforeRelease: true,
				})
				if emitErr != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
				}
				if emitErr == nil && lipapi.OutputCommitted(out) {
					terminal.markOutputCommittedForAttempt(out, attempt, recovery)
				}
				return out, emitErr
			}
			if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
				return lipapi.Event{}, err
			}
		}
		out, recording, emitErr := p.observeClientFacing(ctx, ev, responseEventInput{
			facts: facts, attempt: attempt, recovery: recovery,
			pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: ev.Kind == lipapi.EventResponseFinished,
			finishBeforeRelease: true,
		})
		if emitErr != nil {
			terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
		}
		if emitErr == nil && ev.Kind == lipapi.EventResponseFinished {
			if terminal.isLoopGuardEnabled() {
				// Already verified not held, directly finish without second verifier
				terminal.finishResponse(p, attempt)
				terminal.endALeg(aLegEndBase)
			} else {
				terminal.finishResponseGuarded(ctx, facts.terminalFacts(), attempt, p, ev, "recovery_drain")
			}
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
			// Guard hold check before any success finalization to ensure held B-attempt settles as swallowed (Req 9.1)
			if ev.Kind == lipapi.EventResponseFinished && terminal.isLoopGuardEnabled() {
				outcome := terminal.agentLoopGuardEvaluate(ctx, facts.terminalFacts(), attempt, p, ev)
				held := outcome.Action == stopguard.ActionContinueLeg && !outcome.HoldReleased
				if held {
					if terminal.tryGuardContinuation(ctx, s, attempt, outcome) {
						continue
					}
					if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
						return lipapi.Event{}, err
					}
					terminal.settleSwallowedBAttempt(ctx, attempt)
					if terminal.log != nil {
						terminal.log.DebugContext(ctx, "agent_loop_guard_hold", "source", "gate_drain", "reason", boundGuardReason(outcome.Reason+" "+guardContinuationPendingReason))
					}
					fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: guardContinuationPendingReason}
					terminal.finishResponse(p, attempt)
					attempt.accounting.observeClientEvent(p.nowTime(), fallback)
					pm, _ := facts.hookMeta(attempt.bleg, attempt.cand)
					out, recording, emitErr := p.observeClientFacing(ctx, fallback, responseEventInput{
						facts: facts, attempt: attempt, recovery: recovery,
						pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishBeforeRelease: true,
					})
					if emitErr != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
					}
					if emitErr == nil && lipapi.OutputCommitted(out) {
						terminal.markOutputCommittedForAttempt(out, attempt, recovery)
					}
					return out, emitErr
				}
				if err := terminal.deactivateGuardOverlay(ctx, facts.aLegID); err != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
					return lipapi.Event{}, err
				}
			}
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
				if terminal.isLoopGuardEnabled() {
					// Early hold check already handled held case; this is not held
					if p != nil {
						attempt.recordAttemptLogged(ctx, recordAttemptParams{
							ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess,
						}, facts.attemptDiagAttrs(attempt))
					}
					terminal.finishResponse(p, attempt)
				} else {
					if p != nil {
						attempt.recordAttemptLogged(ctx, recordAttemptParams{
							ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess,
						}, facts.attemptDiagAttrs(attempt))
					}
					terminal.finishResponseGuarded(ctx, facts.terminalFacts(), attempt, p, ev, "gate_drain")
				}
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
			if terminal != nil && terminal.hasALeg() {
				if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, "a-leg canceled", false)
					terminal.endALeg(aLegEndBase)
					return lipapi.Event{}, scopeErr
				}
			}
			if terminal.committed() && p.recordingBlocksReplacement() && p.secureRecordingMandatory {
				if err := terminal.terminalizeGateReplacement(ctx, facts.terminalFacts(), slot.require(), p); err != nil {
					return lipapi.Event{}, err
				}
			}
			plan, err := recovery.tryReplacementIteration(ctx, facts.terminalFacts(), attempt, terminal.committed())
			if err != nil {
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p)
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}
			if !plan.opened {
				return p.keepaliveEvent(), nil
			}
			ready := plan.next
			if err := ready.Prepare(ctx, facts, p, terminal.committed()); err != nil {
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p)
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}
			if err := terminal.registerReplacement(ctx, plan.open, ready); err != nil {
				ready.Dispose(ctx, err)
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p)
				terminal.endALeg(aLegEndBase)
				return lipapi.Event{}, err
			}
			clearAttemptToolState(p, attempt)

			_, published := slot.swapIfOpen(ready)
			if !published {
				// Disposal of unconsumed ready attempt must invoke complete attempt terminalization
				ready.Dispose(ctx, errors.New("publication closed"))
				return p.keepaliveEvent(), nil
			}

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
				terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, "a-leg canceled", false)
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
			ev, cont, err := handleEOF()
			if cont {
				continue
			}
			return ev, err
		}
		ev, cont, err := handleError(recvCtx, err, idleDeadline, ttftDeadline)
		if cont {
			continue
		}
		return ev, err
	}
}
