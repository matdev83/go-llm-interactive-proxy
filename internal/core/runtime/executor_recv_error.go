package runtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
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
	s.resetToolFinal()
	if idleDeadline.expired(recvCtx, err) && s.recoverPolicy != nil {
		dec := s.recoverPolicy.DecideIdle(s.now())
		if dec.Kind == streamrecovery.DecisionFinishPostOutput {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptSuccess,
				Reason:    dec.Reason,
				DetailErr: context.DeadlineExceeded,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			if c := s.takeAndNilInner(); c != nil {
				s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone, Detail: dec.Reason})
			}
			if dec.Warning.Kind != "" {
				s.recoverDrain = append(s.recoverDrain, dec.Warning)
			}
			s.recoverDrain = append(s.recoverDrain, dec.Finish)
			// Defer response_finished authority finalization to the recoverDrain drain path on the
			// next Recv call, matching handleRecvEOF's single-owner invariant. Surface the head
			// event (the warning when present) via emitClientFacingObserved and keep the finish in
			// recoverDrain; when the head is the finish itself, re-queue it and return a zero event
			// so the next Recv call's drain path finalizes via finalizeResponseFinishedAuthority and
			// emits the synthesized usage_delta (the client-reporting consistency fix). Return
			// cont=false so Recv returns to the caller and re-enters at the recoverDrain drain check;
			// a continue would skip that check and wrongly drive a replacement iteration.
			ev := s.recoverDrain[0]
			s.recoverDrain = s.recoverDrain[1:]
			if ev.Kind == lipapi.EventResponseFinished {
				s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
				return lipapi.Event{}, false, nil
			}
			pm, _ := s.recvHookMeta()
			out, emitErr := s.emitClientFacingObserved(ctx, ev, pm)
			return out, false, emitErr
		}
		if dec.Kind == streamrecovery.DecisionRecoverPreOutput {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    dec.Reason,
				DetailErr: dec.Err,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			if c := s.takeAndNilInner(); c != nil {
				s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone, Detail: dec.Reason})
			}
			s.excluded[s.cand.Key] = struct{}{}
			return lipapi.Event{}, true, nil
		}
	}
	if ttftDeadline.expired(recvCtx, err) && !s.isCommitted() {
		ttftScope := ttftDeadline.scope
		if ttftScope == ttftTimeoutLeaf {
			tf := ttftFailure(ttftScope, s.cand.Key)
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    ttftAttemptReason(ttftScope),
				DetailErr: tf,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
			if c := s.takeAndNilInner(); c != nil {
				if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
					s.executor.Log.DebugContext(
						ctx, "retry_recv inner stream close",
						"reason", "leaf_ttft_timeout",
						"error", cerr,
					)
				}
			}
			s.excluded[s.cand.Key] = struct{}{}
			return lipapi.Event{}, true, nil
		}
		tf := ttftFailure(ttftScope, s.cand.Key)
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.aLegID,
			BLeg:      s.bleg,
			Cand:      s.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    ttftAttemptReason(ttftScope),
			DetailErr: tf,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		if c := s.takeAndNilInner(); c != nil {
			if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
				s.executor.Log.DebugContext(
					ctx, "retry_recv inner stream close",
					"reason", "global_ttft_timeout",
					"error", cerr,
				)
			}
		}
		s.runStreamTerminal(ctx, sdkterminal.CommandTimeout, func(cctx context.Context) error {
			s.authority.finalizeIncurredOrRelease(cctx, authorityapp.ReleaseKindLosing, s.operatorUsageForFinalize())
			s.finishFinalStreamObservation(cctx, response.OutcomeFailed)
			s.markFinished()
			return nil
		})
		if !s.isFinished() {
			s.markFinished()
		}
		s.finishALegScope()
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
			ALegID:    s.aLegID,
			BLeg:      s.bleg,
			Cand:      s.cand,
			Outcome:   lipapi.AttemptCancelled,
			Reason:    reason,
			DetailErr: err,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		if c := s.takeAndNilInner(); c != nil {
			if s.aScope != nil {
				_ = s.aScope.Cancel(ctx, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
			} else {
				s.cancelAndCloseInner(ctx, c, leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone})
			}
		}
		cmd := sdkterminal.CommandCancel
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
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
		return lipapi.Event{}, false, err
	}
	if s.isCommitted() || !lipapi.IsRecoverablePreOutput(err) {
		surfErr := err
		if s.isCommitted() && lipapi.IsRecoverablePreOutput(err) {
			surfErr = &lipapi.UpstreamFailureError{
				Phase:        lipapi.PhasePostOutput,
				Recoverable:  false,
				Reason:       attemptReasonDetail(err),
				CandidateKey: s.cand.Key,
			}
		}
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.aLegID,
			BLeg:      s.bleg,
			Cand:      s.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    attemptReasonDetail(surfErr),
			DetailErr: surfErr,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		cmd := sdkterminal.CommandPartialError
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			cmd = sdkterminal.CommandPanic
		}
		s.runStreamTerminal(ctx, cmd, func(cctx context.Context) error {
			s.recordPartialTokenAccounting(cctx, attemptReasonDetail(surfErr), surfErr)
			s.finishFinalStreamObservation(cctx, response.OutcomeFailed)
			s.markFinished()
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
		diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID},
		slog.String("candidate_key", s.cand.Key),
		slog.String("phase", "recv"),
	)
	s.executor.recordAttemptLogged(ctx, recordAttemptParams{
		ALegID:    s.aLegID,
		BLeg:      s.bleg,
		Cand:      s.cand,
		Outcome:   lipapi.AttemptSwallowedFailure,
		Reason:    "recoverable pre-output (recv)",
		DetailErr: err,
	}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
	// Recoverable pre-output failover terminalizes only the attempt plane for
	// ledger/unreserved evidence, then resets. Request stays open; tryReplacement
	// owns the reservation release via a fresh attempt terminal.
	s.runAttemptTerminal(ctx, sdkterminal.CommandSwallowedAttempt, func(cctx context.Context) error {
		// A swallowed pre-output attempt must release its strict reservation for
		// failover, but any advisory/unreserved rules still need the observed usage
		// fact. Apply only the unreserved projection here; do not settle the
		// reservation before the replacement decision.
		usageEv := s.operatorUsageForFinalize()
		s.authority.ApplyUnreservedUsage(cctx, authorityapp.SettlementKindPartial, usageEv)
		s.emitBackendEgressMeteringFact(cctx, metering.AttemptOutcomeFailed, metering.SurfacedNo, usageEv)
		return nil
	})
	s.resetAttemptTerminal()
	if c := s.takeAndNilInner(); c != nil {
		if cerr := c.Close(); cerr != nil && s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(
				ctx, "retry_recv inner stream close",
				"reason", "recoverable_pre_output",
				"error", cerr,
			)
		}
	}
	s.excluded[s.cand.Key] = struct{}{}
	return lipapi.Event{}, true, nil
}
