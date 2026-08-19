package runtime

// Per-event recv handlers for retryRecvStream. Stream lifecycle (Recv,
// tryReplacementIteration, Close), the support surface (completion gates,
// stream-evidence seam, traffic emission), and the type definition itself
// remain in executor_retry_stream.go; this file owns the two per-event
// branches the inner loop dispatches to when a backend event arrives
// successfully or with io.EOF.

import (
	"context"
	"errors"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	completion "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// handleRecvSuccess is the per-event success path dispatched by Recv. It runs
// the per-event accounting, traffic, and hook setup shared by every branch,
// then fans out to one of three local helpers: the tool-event path (which
// may short-circuit on reactor swallow or policy error), the completion-gates
// path, or the no-gates response_finished path. The no-gates non-finished
// default falls through inline because it shares its observation and emit
// steps with the response_finished path.
func (s *retryRecvStream) handleRecvSuccess(ctx context.Context, ev lipapi.Event) (lipapi.Event, bool, error) {
	attempt := s.attempt.require()
	recvAt := s.now()
	attempt.accounting.observeBackendEvent(recvAt, ev)
	// A provider may repeat an already-drained sideband key on a retrying
	// transport. Consume it for neither the canonical stream nor accounting.
	if ev.Kind == lipapi.EventUsageDelta && ev.Accounting.DedupeKey != "" && !s.rememberUsageEvidenceOnceForAttempt(attempt, ev) {
		return lipapi.Event{}, true, nil
	}
	attempt.accounting.observeUsage(ev)
	pm, _ := s.recvHookMeta()
	s.emitTrafficBTP(ctx, ev, pm)
	s.emitUsage(ctx, ev)

	if attempt.toolFinal != nil && attempt.toolFinal.enabled() {
		meta := toolcall.Meta{
			TraceID:    s.facts.traceID,
			ALegID:     s.facts.aLegID,
			BLegID:     attempt.bleg.BLegID,
			AttemptSeq: attempt.bleg.Seq,
		}
		held, ferr := attempt.toolFinal.ingest(ctx, ev, meta)
		if ferr != nil {
			s.resetToolFinal()
			return lipapi.Event{}, false, ferr
		}
		if held {
			return lipapi.Event{}, true, nil
		}
	}

	return s.dispatchClientFacingEvent(ctx, ev)
}

// dispatchClientFacingEvent runs tool policy/reactors, response-part hooks,
// completion gates, client accounting, and PTC for one client-facing event.
// Used for both live backend events (after BTP) and finalized drain replay.
func (s *retryRecvStream) dispatchClientFacingEvent(ctx context.Context, ev lipapi.Event) (lipapi.Event, bool, error) {
	attempt := s.attempt.require()
	pm, tm := s.recvHookMeta()

	var sourceID string
	var sourceFinished bool
	isToolEvent := false
	if te, ok := lipapi.ToolEventFromEvent(ev); ok {
		isToolEvent = true
		sourceID = te.ToolCallID
		sourceFinished = te.Kind == lipapi.ToolEventFinished
		s.toolClass.enrich(&te)
		nextEv, swallowed, err := s.handleToolEventPath(ctx, te, ev, tm)
		if err != nil || swallowed {
			if swallowed && sourceFinished {
				s.toolClass.forget(sourceID)
			}
			return lipapi.Event{}, swallowed, err
		}
		ev = nextEv
	}

	evp := ev
	if herr := s.bus.RunResponsePartHooks(ctx, &evp, pm); herr != nil {
		s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(herr), herr)
		return lipapi.Event{}, false, herr
	}
	ev = evp

	if isToolEvent {
		s.toolClass.observeFinalName(sourceID, ev)
		if sourceFinished {
			s.toolClass.forget(sourceID)
		}
	}

	if gates := s.completionGatesFromContext(ctx); len(gates) > 0 {
		return s.handleGatedPath(ctx, gates, ev, pm)
	}

	attempt.accounting.observeClientEvent(s.now(), ev)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	if ev.Kind == lipapi.EventResponseFinished {
		return s.handleResponseFinishedPath(ctx, ev, pm)
	}
	out, err := s.emitClientFacingObserved(ctx, ev, pm)
	return out, false, err
}

// handleToolEventPath runs tool policies, tool reactors, remembers the
// effective (post-reactor) classification under the source ToolCallID, and
// merges a replacement event back into the recv event when the reactor
// produced one. It returns the (possibly merged) event to continue dispatch
// with, a swallowed bool when the reactor asked to drop the event, or a
// non-nil error from policy or reactor execution.
func (s *retryRecvStream) handleToolEventPath(ctx context.Context, te lipapi.ToolEvent, ev lipapi.Event, tm sdk.ToolMeta) (lipapi.Event, bool, error) {
	attempt := s.attempt.snapshot()
	if err := s.applyToolPolicies(ctx, te, tm); err != nil {
		if attempt != nil {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptSurfacedFailure,
				Reason:    attemptReasonDetail(err),
				DetailErr: err,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
		}
		s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(err), err)
		return lipapi.Event{}, false, err
	}
	res := s.bus.ApplyToolReactors(ctx, te, tm)
	if res.Err != nil {
		s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(res.Err), res.Err)
		return lipapi.Event{}, false, res.Err
	}
	if !res.Emit {
		return lipapi.Event{}, true, nil
	}
	s.toolClass.rememberEffective(te.ToolCallID, res.Event)
	if res.Event.Kind != "" {
		ev = lipapi.MergeToolEventInto(ev, res.Event)
	}
	return ev, false, nil
}

// handleGatedPath runs the completion-gates buffer-and-replace pipeline, then
// observes and emits the drained event. It owns the per-event traffic
// emission for the gated branch.
func (s *retryRecvStream) handleGatedPath(ctx context.Context, gates []completion.Gate, ev lipapi.Event, pm sdk.PartMeta) (lipapi.Event, bool, error) {
	attempt := s.attempt.require()
	out, gerr := s.completionGatedEmit(ctx, gates, ev)
	if errors.Is(gerr, errGateContinueInner) {
		return lipapi.Event{}, true, nil
	}
	if gerr != nil {
		s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(gerr), gerr)
		return lipapi.Event{}, false, gerr
	}
	// A gated completion that drains a response_finished finalizes authority through the single
	// finalizeResponseFinishedAuthority chokepoint (settle, with a losing release fallback when the
	// settle did not mark the reservation settled), matching the no-gates handleResponseFinishedPath.
	// Without this the reservation stays locked until the accounting window resets.
	finishPreflighted := false
	if out.Kind == lipapi.EventResponseFinished && (s.terminal == nil || !s.terminal.accountingFinalized()) {
		if err := s.mandatoryClientFacingPreflight(ctx, out); err != nil {
			return lipapi.Event{}, false, err
		}
		finishPreflighted = true
		usageEv, ok, err := s.finalizeResponseFinishedAuthority(ctx, out)
		if err != nil {
			return lipapi.Event{}, false, err
		}
		if ok {
			s.recoverDrain = append([]lipapi.Event{out}, s.recoverDrain...)
			ev, emitErr := s.emitSynthesizedUsage(ctx, usageEv)
			return ev, false, emitErr
		}
	}
	out = s.emitGateDrained(ctx, out)
	attempt.accounting.observeClientEvent(s.now(), out)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(out, s.now())
	}
	if finishPreflighted {
		// Evidence already recorded in mandatoryClientFacingPreflight; still
		// observe + remember/emit without re-running beforeEmit.
		if s.executor != nil {
			if err := extensions.RunFinalStreamObservationStage(ctx, s.executor.Log, s.executor.ExtensionMetrics, attempt.finalStreamObs, out, s.isCommitted()); err != nil {
				s.finishFinalStreamObservation(ctx, response.OutcomeFailed)
				s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(err), err)
				return lipapi.Event{}, false, err
			}
		}
		releaseDispatch := s.emitTrafficPTCFinal(ctx, &out, pm)
		s.rememberClientEvent(out)
		if out.Kind == lipapi.EventResponseFinished {
			s.commitSuccessfulTurn()
			s.finishFinalStreamObservation(ctx, response.OutcomeSuccessReleased)
		}
		s.commitAffinityIfOutput(ctx, out)
		s.notifyCompactionAfterRelease(ctx, out, releaseDispatch)
		return out, false, nil
	}
	out, err := s.emitClientFacingObserved(ctx, out, pm)
	return out, false, err
}

// handleResponseFinishedPath finalizes the response_finished branch: token
// accounting finalization, drain queuing for the synthesized usage event,
// and attempt success recording. Mandatory encoder/evidence preflight competes
// before NormalFinish settlement so encoder failure can own the terminal.
func (s *retryRecvStream) handleResponseFinishedPath(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) (lipapi.Event, bool, error) {
	attempt := s.attempt.require()
	if err := s.mandatoryClientFacingPreflight(ctx, ev); err != nil {
		return lipapi.Event{}, false, err
	}
	usageEv, ok, err := s.finalizeResponseFinishedAuthority(ctx, ev)
	if err != nil {
		if !s.isFinished() {
			s.markFinished()
		}
		return lipapi.Event{}, false, err
	}
	if ok {
		s.rememberClientEvent(ev)
		s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
		ev, err := s.emitSynthesizedUsage(ctx, usageEv)
		return ev, false, err
	}
	s.executor.recordAttemptLogged(ctx, recordAttemptParams{
		ALegID:  s.facts.aLegID,
		BLeg:    attempt.bleg,
		Cand:    attempt.cand,
		Outcome: lipapi.AttemptSuccess,
	}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
	s.commitSuccessfulTurn()
	s.markFinished()
	s.finishALegScope()
	// Evidence already recorded in mandatoryClientFacingPreflight; still observe
	// + remember/emit without re-running beforeEmit (NormalFinish already competed).
	if s.executor != nil {
		if obsErr := extensions.RunFinalStreamObservationStage(ctx, s.executor.Log, s.executor.ExtensionMetrics, attempt.finalStreamObs, ev, s.isCommitted()); obsErr != nil {
			s.finishFinalStreamObservation(ctx, response.OutcomeFailed)
			s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(obsErr), obsErr)
			return lipapi.Event{}, false, obsErr
		}
	}
	releaseDispatch := s.emitTrafficPTCFinal(ctx, &ev, pm)
	s.rememberClientEvent(ev)
	s.finishFinalStreamObservation(ctx, response.OutcomeSuccessReleased)
	s.commitAffinityIfOutput(ctx, ev)
	s.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, false, nil
}

// mandatoryClientFacingPreflight runs beforeEmitClientFacing before NormalFinish.
// On mandatory failure it claims FrontendEncoderFailure (competing with Close/cancel)
// and settles partial accounting when the attempt is not yet settled.
func (s *retryRecvStream) mandatoryClientFacingPreflight(ctx context.Context, ev lipapi.Event) error {
	err := s.beforeEmitClientFacing(ctx, ev)
	if err == nil {
		return nil
	}
	if s.executor == nil || !s.executor.SecureSessionRecordingMandatory {
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
		return nil
	}
	s.finishFinalStreamObservation(ctx, response.OutcomeFailed)
	s.terminalizePartialFailure(ctx, sdkterminal.CommandFrontendEncoderFailure, attemptReasonDetail(err), err)
	return err
}

// terminalizePartialFailure routes mid-stream / finish-adjacent failures through the
// request terminal so settle/release/facts run once under the owner.
func (s *retryRecvStream) terminalizePartialFailure(ctx context.Context, cmd sdkterminal.Command, reason string, cause error) {
	attempt := s.attempt.require()
	_ = s.runStreamTerminalForAttempt(ctx, cmd, attempt, func(cctx context.Context) error {
		if !attempt.authority.Settled() {
			s.recordPartialTokenAccounting(cctx, attempt, reason, cause)
		}
		s.markFinished()
		return nil
	})
	if !s.isFinished() {
		s.markFinished()
	}
}

func (s *retryRecvStream) handleRecvEOF(ctx context.Context) (lipapi.Event, error) {
	attempt := s.attempt.require()
	s.resetToolFinal()
	// Truncated upstream: never run completion gates on a partial buffer (replace gates could
	// synthesize response_finished and mask the failure).
	gates := s.completionGatesFromContext(ctx)
	if len(gates) > 0 && !s.gateLive && len(s.gateBuf) > 0 && !extensions.StreamFinished(s.gateBuf) {
		s.gateBuf = nil
	}
	if s.recoverPolicy != nil {
		dec := s.recoverPolicy.DecideEOF(io.EOF, s.now())
		if dec.Kind == streamrecovery.DecisionFinishPostOutput {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID:    s.facts.aLegID,
				BLeg:      attempt.bleg,
				Cand:      attempt.cand,
				Outcome:   lipapi.AttemptSuccess,
				Reason:    dec.Reason,
				DetailErr: io.EOF,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
			// Defer finalizeTokenAccounting and settle to the downstream drain path in Recv.
			// The drain path is the single owner of token-accounting finalization and authority
			// settlement for response_finished, so the Finish event stays in recoverDrain until
			// the drain path pops it. We surface the head event (a warning when present) and
			// re-queue it in front of the Finish so the caller observes the same ordering, and
			// the Finish always remains in recoverDrain for finalization. When no warning is
			// produced we return a zero event so the caller knows to call Recv again rather
			// than mistakenly treating the Finish as already-handled.
			if dec.Warning.Kind != "" {
				s.recoverDrain = append(s.recoverDrain, dec.Warning)
			}
			s.recoverDrain = append(s.recoverDrain, dec.Finish)
			ev := s.recoverDrain[0]
			s.recoverDrain = s.recoverDrain[1:]
			if ev.Kind == lipapi.EventResponseFinished {
				s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
				return lipapi.Event{}, nil
			}
			pm, _ := s.recvHookMeta()
			return s.emitClientFacingObserved(ctx, ev, pm)
		}
	}
	if !s.isFinished() {
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.facts.aLegID,
			BLeg:      attempt.bleg,
			Cand:      attempt.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    "stream ended without response_finished",
			DetailErr: io.EOF,
		}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
	}
	s.runStreamTerminalForAttempt(ctx, sdkterminal.CommandEOF, attempt, func(cctx context.Context) error {
		s.recordPartialTokenAccounting(cctx, attempt, "stream ended without response_finished", io.EOF)
		s.finishFinalStreamObservation(cctx, response.OutcomeFailed)
		s.markFinished()
		return nil
	})
	if !s.isFinished() {
		s.markFinished()
	}
	s.finishALegScope()
	return lipapi.Event{}, io.EOF
}
