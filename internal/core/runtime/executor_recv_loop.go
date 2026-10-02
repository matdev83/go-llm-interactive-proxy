package runtime

// Recv-phase inner-loop control for retryRecvStream. Stream lifecycle
// helpers (Close, handleRecvSuccess, handleRecvEOF,
// etc.) remain in executor_retry_stream.go; this file owns the inner-loop
// state machine that drives per-recv failover within an attempt's budget.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

// errGateContinueInner signals Recv to pull another inner event without returning to the client yet.
var errGateContinueInner = errors.New("runtime: completion gate continue buffering")

// pendingOrdinaryHead picks the release head for one finish route: the
// authoritative completion-gate output when preparation produced one, and the
// accepted finish otherwise. Candidate existence is independent of the ordinary
// head, so a suppressed result still releases its own evaluated sequence.
func pendingOrdinaryHead(finish, ordinary lipapi.Event) lipapi.Event {
	if ordinary.Kind != "" {
		return ordinary
	}
	return finish
}

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
	// pendingExpectedFence is the ONE lexical publication fence for this receive
	// call. It decides, for one expected candidate and the origin it was retained
	// under, whether the private publication may still be staged, activated, or
	// physically delivered.
	//
	// It checks the complete identity, not a partial one: the response still
	// retains the SAME candidate value, that value's origin is the SAME attempt,
	// the frozen B-leg ID and sequence the candidate was copied under still match
	// that attempt, the live slot still publishes that same attempt, the live
	// caller context is alive, the shared A-leg carries no authoritative cause, and
	// the attempt publication window is still open. An ordinary request owner that
	// already reached Released is EXPECTED here and is not a withdrawal fence.
	//
	// The same predicate is handed to the finish authority seams and consulted at
	// every publication boundary, so staging, activation, and delivery can never
	// disagree about which identity they are publishing. It performs no I/O and
	// holds no owner, pipeline, or control mutex.
	pendingExpectedFence := func(expected *pendingCompletion, origin *attemptSession) bool {
		if p == nil || expected == nil || origin == nil {
			return false
		}
		if p.pendingPreparedSnapshot() != expected {
			return false
		}
		if expected.origin != origin {
			return false
		}
		if !pendingOriginOwns(expected, origin) {
			return false
		}
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		if slot.publicationIsClosed() {
			return false
		}
		if slot.snapshot() != origin {
			return false
		}
		if terminal != nil && terminal.hasALeg() && terminal.aLegErr() != nil {
			return false
		}
		return true
	}
	// drainPending delivers exactly one already-observed publication event to the
	// client under the FULL identity fence, and converges the accepted-finish
	// bookkeeping at the ACTUAL finish delivery.
	//
	// One delivery sequences several owners: the response owner that owns the
	// retained candidate and its queue, the terminal owner that converges the
	// accepted finish, the recovery owner whose policy must observe the delivery,
	// and the live attempt slot. It therefore stays a lexical coordinator here, over
	// the references Recv already holds, rather than becoming a new carrier, a
	// receiver, or a helper that repackages the same owner graph. It carries no new
	// state: the live Recv context and every owner it sequences are captured for
	// this call only, so no persistent drain state holds a context.
	//
	// Release-tail effects run outside every mutex, and the fence is rechecked
	// after them so a withdrawal discards the remainder instead of returning content
	// the client must never see.
	drainPending := func(expected *pendingCompletion, origin *attemptSession) (lipapi.Event, bool, error) {
		if p == nil || expected == nil || origin == nil {
			return lipapi.Event{}, false, nil
		}
		if !pendingExpectedFence(expected, origin) {
			// A failed fence is the withdrawal of the candidate THIS delivery was
			// pinned to, and only of that one. A caller that owns no private
			// publication, or that is delivering some other live candidate, must not
			// erase it: the identity fence and the ownership rule are separate facts.
			if p.pendingPreparedSnapshot() == expected {
				p.withdrawPendingCompletion(origin)
			}
			return lipapi.Event{}, false, nil
		}
		ev, ok := p.pendingPublicationHead()
		if !ok {
			return lipapi.Event{}, false, nil
		}
		isUsage := p.pendingPublicationNextUsage()
		p.popPendingCompletionRelease()

		// The private drain owns its own physical delivery, so the ordinary dispatch
		// seam's client-event effects must run here exactly once per DELIVERED event,
		// including the accepted finish. Without them the attempt accounting and the
		// recovery policy would never see the published result or the physical finish,
		// because the raw finish was recorded and finalized without being released.
		// They run on delivery only, never while staging, and the event is consumed
		// above so a requeued finish is never observed twice.
		origin.observeAccountingClientEvent(p.nowTime(), ev)
		if recovery != nil && recovery.recoverPolicy != nil {
			recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
		}

		// Release tail: the effects that must run exactly once per DELIVERED event.
		// Local customer observation, PTC traffic, the client accumulator, affinity,
		// and compaction notification run here for the first time; the mandatory
		// recorder and the fail-closed final observer already ran once during
		// preflight, so neither repeats.
		out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{
			facts: facts, attempt: origin, recovery: recovery,
			pm:        pendingReleasePartMeta(facts, origin, facts.terminalFacts()),
			committed: terminal.committed(), now: p.nowTime(),
			recorded: true, finalObserved: true,
		})
		if err != nil {
			p.withdrawPendingCompletion(origin)
			return lipapi.Event{}, false, err
		}
		if isUsage {
			p.emitUsageTerminal(ctx, facts.terminalFacts(), origin, out)
		}
		// The fence is rechecked AFTER the external release effects so a Close, an A-leg
		// cancellation, or a caller cancellation that won while they ran discards the
		// remainder instead of returning content.
		if !pendingExpectedFence(expected, origin) {
			p.withdrawPendingCompletion(origin)
			return lipapi.Event{}, false, nil
		}
		if ev.Kind == lipapi.EventResponseFinished {
			origin.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID: facts.aLegID, BLeg: origin.bleg, Cand: origin.cand, Outcome: lipapi.AttemptSuccess,
			}, facts.attemptDiagAttrs(origin))
			p.commitSuccessfulTurn(facts, origin, terminal.committed())
			// A fully delivered publication closes its observer successfully, exactly
			// once, at this boundary.
			p.finishFinalStreamObservation(ctx, origin, response.OutcomeSuccessReleased)
			terminal.finishResponseAtBoundary(p, origin, p.pendingPublicationEndALeg())
			// The accepted finish is the real end of the owned window: the retained
			// candidate, its reservation, and its queue are disposed HERE and only here,
			// so no later Recv can deliver a second copy and a competing attempt can
			// never inherit a finished publication.
			p.completePendingPublication(origin)
			return out, true, nil
		}
		if lipapi.OutputCommitted(out) {
			terminal.markOutputCommittedForAttempt(out, origin, recovery)
		}
		return out, true, nil
	}
	// drainPendingFinish is the finish-route adapter over drainPending: an error
	// propagates, an exhausted drain means the caller ends the stream instead of
	// releasing the accepted finish a second time, and one drained event is returned
	// to the client. Its zero event is strictly this internal result paired with
	// continueInner=true and never a public (zero, nil) return.
	drainPendingFinish := func(expected *pendingCompletion, origin *attemptSession) (lipapi.Event, bool, error) {
		ev, more, err := drainPending(expected, origin)
		if err != nil {
			return lipapi.Event{}, false, err
		}
		if !more {
			return lipapi.Event{}, true, nil
		}
		return ev, false, nil
	}
	dispatchClientFacingEvent := func(ev lipapi.Event, prepared recvEventPreparation) (lipapi.Event, bool, error) {
		attempt := slot.require()
		transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
		if transformed.err != nil {
			if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, transformed.err) {
				return lipapi.Event{}, true, nil
			}
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
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, gated.recording.mandatory(), gated.err) {
					return lipapi.Event{}, true, nil
				}
				return lipapi.Event{}, false, gated.err
			}
			ev = gated.event
			if gated.finishPreflight {
				// The expected candidate and origin are captured here, before the
				// terminal runs, and the SAME pair pins the physical drain below.
				hooks := recvFinishAuthorityInput(ctx, s, attempt, gated.pending, false, pendingExpectedFence)
				usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
				if errors.Is(err, errTerminalDecisionContinuationPublished) {
					return lipapi.Event{}, true, nil
				}
				if err != nil {
					return lipapi.Event{}, false, err
				}
				if p.pendingPublicationActive() {
					// The accepted normal terminal staged and activated the private
					// batch. The turn is NOT physically finished yet: the accepted
					// finish bookkeeping converges at the actual finish delivery.
					return drainPendingFinish(hooks.expectedPrepared, attempt)
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
				terminal.finishResponseAtBoundary(p, attempt, false)
			}
			attempt.observeAccountingClientEvent(p.nowTime(), ev)
			if recovery != nil && recovery.recoverPolicy != nil {
				recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
			}
			if gated.finishPreflight {
				out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
				if err != nil {
					if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err) {
						return lipapi.Event{}, true, nil
					}
					return lipapi.Event{}, false, err
				}
				if out.Kind == lipapi.EventResponseFinished {
					p.commitSuccessfulTurn(facts, attempt, terminal.committed())
				}
				return out, false, nil
			}
			out, recording, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
			if err != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err) {
					return lipapi.Event{}, true, nil
				}
				return lipapi.Event{}, false, err
			}
			return out, false, nil
		}
		// The ordinary client-event effects of the accepted finish run only when this
		// response owns NO private candidate, and preparation decides that: while a
		// candidate exists the raw finish is still a CANDIDATE, and a rejected,
		// continued, or withdrawn publication must not stamp a client finish, complete
		// the proxy, or report finished output for a batch that is never released. The
		// private drain runs those same effects exactly once at the physical delivery.
		var prep pendingPreparation
		pendingOwned := false
		if ev.Kind == lipapi.EventResponseFinished {
			prepared, pendingErr := p.preparePendingCompletion(ctx, facts, attempt, ev, nil, terminal.committed())
			if pendingErr != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, pendingErr) {
					return lipapi.Event{}, true, nil
				}
				return lipapi.Event{}, false, pendingErr
			}
			prep, pendingOwned = prepared, prepared.prepared.holds()
			// An authoritative completion-gate output that carries no accepted finish
			// becomes the ordinary release head here, so the ordinary gate path drains
			// it and the chain is never evaluated a second time.
			ev = pendingOrdinaryHead(ev, prep.ordinary)
		}
		if !pendingOwned {
			attempt.observeAccountingClientEvent(p.nowTime(), ev)
			if recovery != nil && recovery.recoverPolicy != nil {
				recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
			}
		}
		if ev.Kind == lipapi.EventResponseFinished {
			recording := responseRecordingResult{}
			if !prep.prepared.holds() {
				recording = p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
				if recording.mandatory() {
					if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err) {
						return lipapi.Event{}, true, nil
					}
					return lipapi.Event{}, false, recording.err
				}
			}
			// The expected candidate and origin are captured here, before the terminal
			// runs, and the SAME pair pins the physical drain below.
			hooks := recvFinishAuthorityInput(ctx, s, attempt, prep.prepared, true, pendingExpectedFence)
			usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
			if errors.Is(err, errTerminalDecisionContinuationPublished) {
				return lipapi.Event{}, true, nil
			}
			if err != nil {
				if !terminal.finished() {
					terminal.finishResponse(p, attempt)
				}
				return lipapi.Event{}, false, err
			}
			if p.pendingPublicationActive() {
				return drainPendingFinish(hooks.expectedPrepared, attempt)
			}
			if ok {
				p.rememberClientEvent(ev)
				p.prependRecoveryDrain(ev)
				emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
				return emitted, false, emitErr
			}
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
			p.commitSuccessfulTurn(facts, attempt, terminal.committed())
			terminal.finishResponseAtBoundary(p, attempt, true)
			out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
			if err != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err) {
					return lipapi.Event{}, true, nil
				}
				return lipapi.Event{}, false, err
			}
			return out, false, nil
		}
		out, recording, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
		if err != nil {
			if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err) {
				return lipapi.Event{}, true, nil
			}
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
				if terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p) {
					return lipapi.Event{}, true, nil
				}
				fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "post_output_interrupted"}
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
		if terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p) {
			return lipapi.Event{}, true, nil
		}
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
				attempt.setPendingCancelCause(lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: dec.reason})
				fallback := lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: "post_output_interrupted"}
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
		if terminal.committed() || !lipapi.IsRecoverablePreOutput(recvErr) {
			surfErr := recvErr
			if terminal.committed() && lipapi.IsRecoverablePreOutput(recvErr) {
				surfErr = &lipapi.UpstreamFailureError{Phase: lipapi.PhasePostOutput, Recoverable: false, Reason: attemptReasonDetail(recvErr), CandidateKey: attempt.cand.Key}
			}
			if terminal.terminalizeSurfacedFailure(ctx, facts.terminalFacts(), attempt, p, surfErr, backendReceivePanic(recvErr)) {
				return lipapi.Event{}, true, nil
			}
			return lipapi.Event{}, false, surfErr
		}
		facts.logRecoverablePreOutput(ctx, p.log, attempt.cand.Key)
		attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), "recoverable pre-output (recv)", recvErr)
		recovery.exclude(attempt.cand.Key)
		return lipapi.Event{}, true, nil
	}
	// The private publication drains completely before the stream may report EOF,
	// so an accepted result, its refreshed customer usage, and its finish are never
	// stranded. The first drain of this receive ALWAYS selects the retained
	// ORIGINAL origin, never the live slot, and it reads the expected candidate and
	// its frozen origin as one pair under the response-state lock. Every live fence
	// is applied by the drain itself: a withdrawn drain discards its remainder and
	// lets the existing end-of-stream behavior stand.
	if expected, expectedOrigin := p.pendingPublicationExpectation(); expected != nil {
		if ev, more, derr := drainPending(expected, expectedOrigin); derr != nil {
			return lipapi.Event{}, derr
		} else if more {
			return ev, nil
		}
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
			prep, pendingErr := p.preparePendingCompletion(ctx, facts, attempt, ev, nil, terminal.committed())
			if pendingErr != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, pendingErr)
				return lipapi.Event{}, pendingErr
			}
			recording := responseRecordingResult{}
			if !prep.prepared.holds() {
				recording = p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
				if recording.mandatory() {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
					return lipapi.Event{}, recording.err
				}
			}
			// The expected candidate and origin are captured here, before the terminal
			// runs, and the SAME pair pins the physical drain below.
			hooks := recvFinishAuthorityInput(ctx, s, attempt, prep.prepared, true, pendingExpectedFence)
			usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
			if errors.Is(err, errTerminalDecisionContinuationPublished) {
				return lipapi.Event{}, nil
			}
			if err != nil {
				if !terminal.finished() {
					terminal.finishResponseAtBoundary(p, attempt, true)
				}
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
				return lipapi.Event{}, err
			}
			if p.pendingPublicationActive() {
				ev, more, drainErr := drainPending(hooks.expectedPrepared, attempt)
				if drainErr != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, drainErr)
					return lipapi.Event{}, drainErr
				}
				if !more {
					return lipapi.Event{}, io.EOF
				}
				return ev, nil
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
		// A continuation published inside this Recv also updates immutable request
		// facts. Its next candidate must use the same current lineage as its attempt.
		facts = s.facts
		if ev, more, drainErr := drainPending(p.pendingPreparedSnapshot(), attempt); drainErr != nil {
			return lipapi.Event{}, drainErr
		} else if more {
			return ev, nil
		}
		if toolFinal := attempt.toolCallAssembler(); toolFinal != nil {
			if ev, ok := toolFinal.popDrain(); ok {
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
				prep, pendingErr := p.preparePendingCompletion(ctx, facts, attempt, ev, nil, terminal.committed())
				if pendingErr != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, pendingErr)
					return lipapi.Event{}, pendingErr
				}
				recording := responseRecordingResult{}
				if !prep.prepared.holds() {
					recording = p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
					if recording.mandatory() {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
						return lipapi.Event{}, recording.err
					}
				}
				// The expected candidate and origin are captured here, before the
				// terminal runs, and the SAME pair pins the physical drain below.
				hooks := recvFinishAuthorityInput(ctx, s, attempt, prep.prepared, false, pendingExpectedFence)
				usageEv, usageOk, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
				if errors.Is(err, errTerminalDecisionContinuationPublished) {
					continue
				}
				if err != nil {
					if !terminal.finished() {
						terminal.finishResponse(p, attempt)
					}
					return lipapi.Event{}, err
				}
				if p.pendingPublicationActive() {
					out, more, drainErr := drainPending(hooks.expectedPrepared, attempt)
					if drainErr != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, drainErr)
						return lipapi.Event{}, drainErr
					}
					if !more {
						continue
					}
					return out, nil
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
				terminal.finishResponseAtBoundary(p, attempt, false)
			}
			attempt.observeAccountingClientEvent(p.nowTime(), ev)
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
				ready.Dispose(ctx, fmt.Errorf("recv loop replacement: %w", errPublicationClosed))
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
			if p.consumeContinuationLifecycleMarker(ev) {
				continue
			}
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
