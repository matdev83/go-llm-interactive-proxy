package runtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// cancellationAttemptReason returns a low-cardinality bucket for attempt records when
// recv ends due to context cancellation or deadline.
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

// handleRecvError classifies a non-EOF recv error into one of five outcomes:
//   - idle-recovery finish-post-output: append Warning + Finish to recoverDrain and finalize
//     via the drain path (single-owner invariant matches handleRecvEOF).
//   - idle-recovery pre-output swallow: record AttemptSwallowedFailure, drop the inner stream,
//     and signal cont=true so Recv continues with a replacement iteration.
//   - ttft leaf timeout: same swallow behavior, scoped to the leaf candidate.
//   - ttft global timeout: surface ErrTTFTTimeout to the client and release the authority
//     reservation as a losing attempt.
//   - context cancellation / deadline: surface the cancellation error, record AttemptCancelled,
//     and persist cancellation billing.
//   - committed-or-non-recoverable: record AttemptSurfacedFailure, persist partial token
//     accounting, and surface the (possibly wrapped) error.
//   - otherwise (recoverable pre-output, not committed): record AttemptSwallowedFailure,
//     persist partial ledger evidence, drop the inner stream, and signal cont=true.
//
// The cont return value tells Recv whether to continue the inner-loop iteration (true)
// or return the (event, err) pair to the client (false).
func (s *retryRecvStream) handleRecvError(ctx, recvCtx context.Context, err error, idleDeadline idleContextDeadline, ttftDeadline ttftContextDeadline) (lipapi.Event, bool, error) {
	attempt := s.attempt.require()
	clearAttemptToolState(s.responsePipeline, s.attempt.snapshot())
	if idleDeadline.expired(recvCtx, err) && s.recovery != nil && s.recovery.recoverPolicy != nil {
		dec := s.recovery.recoverPolicy.DecideIdle(s.now())
		if dec.Kind == streamrecovery.DecisionFinishPostOutput {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptSuccess,
				Reason:    dec.Reason,
				DetailErr: context.DeadlineExceeded,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
			if c := attempt.takeInner(); c != nil {
				s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone, Detail: dec.Reason})
			}
			if dec.Warning.Kind != "" {
				s.appendRecoveryDrain(dec.Warning)
			}
			s.appendRecoveryDrain(dec.Finish)
			// Defer response_finished authority finalization to the recoverDrain drain path on the
			// next Recv call, matching handleRecvEOF's single-owner invariant. Surface the head
			// event (the warning when present) through the response observation boundary and keep the finish in
			// recoverDrain; when the head is the finish itself, re-queue it and return a zero event
			// so the next Recv call's drain path finalizes via finalizeResponseFinishedAuthority and
			// emits the synthesized usage_delta (the client-reporting consistency fix). Return
			// cont=false so Recv returns to the caller and re-enters at the recoverDrain drain check;
			// a continue would skip that check and wrongly drive a replacement iteration.
			ev, _ := s.popRecoveryDrain()
			if ev.Kind == lipapi.EventResponseFinished {
				s.prependRecoveryDrain(ev)
				return lipapi.Event{}, false, nil
			}
			pm, _ := s.recvHookMeta()
			out, recording, emitErr := s.responsePipeline.observeClientFacing(ctx, ev, responseEventInput{
				facts: s.facts, executor: s.executor, attempt: attempt, recovery: s.recovery,
				pm: pm, committed: s.isCommitted(), now: s.now(), finishBeforeRelease: true,
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
			return out, false, emitErr
		}
		if dec.Kind == streamrecovery.DecisionRecoverPreOutput {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    dec.Reason,
				DetailErr: dec.Err,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
			if c := attempt.takeInner(); c != nil {
				s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone, Detail: dec.Reason})
			}
			s.recovery.exclude(attempt.cand.Key)
			return lipapi.Event{}, true, nil
		}
	}
	if ttftDeadline.expired(recvCtx, err) && !s.isCommitted() {
		ttftScope := ttftDeadline.scope
		if ttftScope == ttftTimeoutLeaf {
			tf := ttftFailure(ttftScope, attempt.cand.Key)
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    ttftAttemptReason(ttftScope),
				DetailErr: tf,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
			if c := attempt.takeInner(); c != nil {
				if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
					s.executor.Log.DebugContext(
						ctx, "retry_recv inner stream close",
						"reason", "leaf_ttft_timeout",
						"error", cerr,
					)
				}
			}
			s.recovery.exclude(attempt.cand.Key)
			return lipapi.Event{}, true, nil
		}
		tf := ttftFailure(ttftScope, attempt.cand.Key)
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.facts.aLegID,
			BLeg:      attempt.bleg,
			Cand:      attempt.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    ttftAttemptReason(ttftScope),
			DetailErr: tf,
		}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
		if c := attempt.takeInner(); c != nil {
			if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
				s.executor.Log.DebugContext(
					ctx, "retry_recv inner stream close",
					"reason", "global_ttft_timeout",
					"error", cerr,
				)
			}
		}
		s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandTimeout, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
			attempt.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindLosing, s.operatorUsageForFinalize())
			s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
			s.markFinished()
			return nil
		}, func(cctx context.Context, _ coreterm.Outcome) error {
			s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandTimeout)
			s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandTimeout)
			return nil
		})
		if !s.isFinished() {
			s.markFinished()
		}
		s.terminal.endALeg(aLegEndBase)
		return lipapi.Event{}, false, lipapi.ErrTTFTTimeout
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		reason := cancellationAttemptReason(ctx, err)
		if s.executor != nil && s.executor.Log != nil && err != nil {
			s.executor.Log.DebugContext(
				ctx, "retry_recv context cancellation",
				"reason", reason,
				"recv_error_detail", diag.TruncErrDetail(err, attemptReasonMaxRunes),
			)
		}
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.facts.aLegID,
			BLeg:      attempt.bleg,
			Cand:      attempt.cand,
			Outcome:   lipapi.AttemptCancelled,
			Reason:    reason,
			DetailErr: err,
		}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
		if c := attempt.takeInner(); c != nil {
			if s.terminal != nil && s.terminal.hasALeg() {
				_ = s.terminal.cancelALeg(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
			} else {
				s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
			}
		}
		cmd := sdkterminal.CommandCancel
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cmd = sdkterminal.CommandTimeout
		}
		s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
			s.persistCancellationBilling(cctx, attempt, reason)
			s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeCancelled)
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
		return lipapi.Event{}, false, err
	}
	if s.isCommitted() || !lipapi.IsRecoverablePreOutput(err) {
		surfErr := err
		if s.isCommitted() && lipapi.IsRecoverablePreOutput(err) {
			surfErr = &lipapi.UpstreamFailureError{
				Phase:        lipapi.PhasePostOutput,
				Recoverable:  false,
				Reason:       attemptReasonDetail(err),
				CandidateKey: attempt.cand.Key,
			}
		}
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.facts.aLegID,
			BLeg:      attempt.bleg,
			Cand:      attempt.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    attemptReasonDetail(surfErr),
			DetailErr: surfErr,
		}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
		cmd := sdkterminal.CommandPartialError
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			cmd = sdkterminal.CommandPanic
		}
		s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
			s.recordPartialTokenAccounting(cctx, attempt, attemptReasonDetail(surfErr), surfErr)
			s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
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
		return lipapi.Event{}, false, surfErr
	}
	var log *slog.Logger
	if s.executor != nil {
		log = s.executor.Log
	}
	diag.LogDecision(
		ctx, log, "recoverable_pre_output_swallowed",
		diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID},
		slog.String("candidate_key", attempt.cand.Key),
		slog.String("phase", "recv"),
	)
	s.executor.recordAttemptLogged(ctx, recordAttemptParams{
		ALegID:    s.facts.aLegID,
		BLeg:      attempt.bleg,
		Cand:      attempt.cand,
		Outcome:   lipapi.AttemptSwallowedFailure,
		Reason:    "recoverable pre-output (recv)",
		DetailErr: err,
	}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
	// Recoverable pre-output failover terminalizes only the attempt plane for
	// ledger/unreserved evidence, then resets. Request stays open; tryReplacement
	// owns the reservation release via a fresh attempt terminal.
	attempt.terminalizeSnapshot(ctx, sdkterminal.CommandSwallowedAttempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		// A swallowed pre-output attempt must release its strict reservation for
		// failover, but any advisory/unreserved rules still need the observed usage
		// fact. Apply only the unreserved projection here; do not settle the
		// reservation before the replacement decision.
		usageEv := s.operatorUsageForFinalize()
		attempt.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindPartial, usageEv)
		s.emitBackendEgressMeteringFactForAttempt(cctx, attempt, metering.AttemptOutcomeFailed, metering.SurfacedNo, usageEv)
		s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandSwallowedAttempt)
		return nil
	})
	if c := attempt.takeInner(); c != nil {
		if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(
				ctx, "retry_recv inner stream close",
				"reason", "recoverable_pre_output",
				"error", cerr,
			)
		}
	}
	s.recovery.exclude(attempt.cand.Key)
	return lipapi.Event{}, true, nil
}
