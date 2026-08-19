package runtime

// Per-event recv handlers for retryRecvStream. Stream lifecycle (Recv,
// tryReplacementIteration, Close), the support surface (completion gates and
// stream-evidence seam), and the type definition itself
// remain in executor_retry_stream.go; this file owns the two per-event
// branches the inner loop dispatches to when a backend event arrives
// successfully or with io.EOF.

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	coreterm "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	completion "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
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
	if ev.Kind == lipapi.EventUsageDelta && ev.Accounting.DedupeKey != "" && !attempt.rememberUsageEvidenceOnce(ev) {
		return lipapi.Event{}, true, nil
	}
	attempt.accounting.observeUsage(ev)
	pm, _ := s.recvHookMeta()
	s.responsePipeline.emitTraffic(ctx, s.executor, attempt, sdktraffic.LegBTP, ev, pm)
	s.responsePipeline.emitUsage(ctx, s.executor, s.facts, attempt, ev)

	if attempt.toolFinal != nil && attempt.toolFinal.enabled() {
		meta := toolcall.Meta{
			TraceID:    s.facts.traceID,
			ALegID:     s.facts.aLegID,
			BLegID:     attempt.bleg.BLegID,
			AttemptSeq: attempt.bleg.Seq,
		}
		held, ferr := attempt.toolFinal.ingest(ctx, ev, meta)
		if ferr != nil {
			clearAttemptToolState(s.responsePipeline, s.attempt.snapshot())
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
	if s.responsePipeline == nil {
		// Production assembly always installs the owner before exposure. Keep
		// direct test fixtures nil-safe while they migrate to owner builders.
		s.responsePipeline = newResponsePipeline()
	}
	attempt := s.attempt.require()
	pm, tm := s.recvHookMeta()

	var sourceID string
	var sourceFinished bool
	isToolEvent := false
	if te, ok := lipapi.ToolEventFromEvent(ev); ok {
		isToolEvent = true
		sourceID = te.ToolCallID
		sourceFinished = te.Kind == lipapi.ToolEventFinished
		s.responsePipeline.enrichToolEvent(&te)
		nextEv, swallowed, err := s.handleToolEventPath(ctx, te, ev, tm)
		if err != nil || swallowed {
			if swallowed && sourceFinished {
				s.responsePipeline.forgetToolClassification(sourceID)
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
		s.responsePipeline.observeToolFinalName(sourceID, ev)
		if sourceFinished {
			s.responsePipeline.forgetToolClassification(sourceID)
		}
	}

	if gates := s.completionGatesFromContext(ctx); len(gates) > 0 {
		return s.handleGatedPath(ctx, gates, ev, pm)
	}

	attempt.accounting.observeClientEvent(s.now(), ev)
	if s.recovery != nil && s.recovery.recoverPolicy != nil {
		s.recovery.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	if ev.Kind == lipapi.EventResponseFinished {
		return s.handleResponseFinishedPath(ctx, ev, pm)
	}
	out, recording, err := s.responsePipeline.observeClientFacing(ctx, ev, responseEventInput{
		facts: s.facts, executor: s.executor, attempt: attempt, recovery: s.recovery,
		pm: pm, committed: s.isCommitted(), now: s.now(), finishBeforeRelease: true,
	})
	if err != nil {
		cmd := sdkterminal.CommandPartialError
		if recording.mandatory() {
			cmd = sdkterminal.CommandFrontendEncoderFailure
		}
		s.terminalizePartialFailure(ctx, cmd, attemptReasonDetail(err), err)
		if !s.isCommitted() {
			s.terminal.endALeg(aLegEndBase)
		}
		return lipapi.Event{}, false, err
	}
	if lipapi.OutputCommitted(out) {
		s.markOutputCommitted(out)
	}
	return out, false, nil
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
	s.responsePipeline.rememberEffectiveTool(te.ToolCallID, res.Event)
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
	snap := s.completionSnapshot(ctx)
	meta := completion.Meta{
		TraceID:    s.facts.traceID,
		ALegID:     s.facts.aLegID,
		BLegID:     attempt.bleg.BLegID,
		AttemptSeq: attempt.bleg.Seq,
	}
	if v, ok := s.viewsFor(ctx); ok {
		meta.Scope = v.Scope
		meta.Session = v.Session
		meta.Workspace = v.Workspace
	}
	svc := completion.Services{}
	if snap != nil {
		svc.State = snap.State()
		svc.Aux = snap.Aux()
	}
	var stageLog *slog.Logger
	if s.executor != nil {
		stageLog = s.executor.Log
	}
	limits := completion.DefaultBufferLimits()
	if s.executor != nil && s.executor.CompletionBufferLimits.MaxEvents > 0 {
		limits = s.executor.CompletionBufferLimits
	}
	out, replaced, gerr := s.responsePipeline.completionGatedEmit(ctx, gates, ev, responseGateInput{
		meta:      meta,
		services:  svc,
		stageLog:  stageLog,
		committed: s.isCommitted(),
		limits:    limits,
	})
	if errors.Is(gerr, errGateContinueInner) {
		return lipapi.Event{}, true, nil
	}
	if gerr != nil {
		s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(gerr), gerr)
		return lipapi.Event{}, false, gerr
	}
	if replaced {
		views, viewsOK := s.viewsFor(ctx)
		if err := s.responsePipeline.cycleFinalStreamObservation(ctx, s.facts, s.executor, attempt, views, viewsOK, response.OutcomeGateReplaced, s.isCommitted()); err != nil {
			return lipapi.Event{}, false, err
		}
	}
	// A gated completion that drains a response_finished finalizes authority through the single
	// finalizeResponseFinishedAuthority chokepoint (settle, with a losing release fallback when the
	// settle did not mark the reservation settled), matching the no-gates handleResponseFinishedPath.
	// Without this the reservation stays locked until the accounting window resets.
	finishPreflighted := false
	if out.Kind == lipapi.EventResponseFinished && (s.terminal == nil || !s.terminal.accountingFinalized()) {
		recording := s.responsePipeline.recordClientFacing(ctx, s.facts, attempt, s.executor, out, s.isCommitted())
		if recording.mandatory() {
			s.responsePipeline.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
			s.terminalizePartialFailure(ctx, sdkterminal.CommandFrontendEncoderFailure, attemptReasonDetail(recording.err), recording.err)
			return lipapi.Event{}, false, recording.err
		}
		if recording.err != nil && s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", recording.err)
		}
		finishPreflighted = true
		usageEv, ok, err := s.finalizeResponseFinishedAuthority(ctx, out)
		if err != nil {
			return lipapi.Event{}, false, err
		}
		if ok {
			s.prependRecoveryDrain(out)
			ev, emitErr := s.emitSynthesizedUsage(ctx, usageEv)
			return ev, false, emitErr
		}
	}
	if lipapi.OutputCommitted(out) {
		s.markCommitted()
	}
	if out.Kind == lipapi.EventResponseFinished {
		if s.executor != nil {
			s.executor.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID: s.facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: lipapi.AttemptSuccess,
			}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
		}
		s.markFinished()
	}
	attempt.accounting.observeClientEvent(s.now(), out)
	if s.recovery != nil && s.recovery.recoverPolicy != nil {
		s.recovery.recoverPolicy.ObserveClientEvent(out, s.now())
	}
	if finishPreflighted {
		// Evidence already recorded in the mandatory preflight; response
		// observation now owns final observer/traffic/evidence ordering.
		out, _, err := s.responsePipeline.observeClientFacing(ctx, out, responseEventInput{
			facts: s.facts, executor: s.executor, attempt: attempt, recovery: s.recovery,
			pm: pm, committed: s.isCommitted(), now: s.now(), recorded: true, finishAfterRemember: true,
		})
		if err != nil {
			s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(err), err)
			return lipapi.Event{}, false, err
		}
		if out.Kind == lipapi.EventResponseFinished {
			s.responsePipeline.commitSuccessfulTurn(s.facts, attempt, s.executor.Keepwarm, s.isCommitted())
		}
		return out, false, nil
	}
	if lipapi.OutputCommitted(out) {
		s.markOutputCommitted(out)
	}
	out, recording, err := s.responsePipeline.observeClientFacing(ctx, out, responseEventInput{
		facts: s.facts, executor: s.executor, attempt: attempt, recovery: s.recovery,
		pm: pm, committed: s.isCommitted(), now: s.now(), finishBeforeRelease: true,
	})
	if err != nil {
		cmd := sdkterminal.CommandPartialError
		if recording.mandatory() {
			cmd = sdkterminal.CommandFrontendEncoderFailure
		}
		s.terminalizePartialFailure(ctx, cmd, attemptReasonDetail(err), err)
		return lipapi.Event{}, false, err
	}
	return out, false, nil
}

// handleResponseFinishedPath finalizes the response_finished branch: token
// accounting finalization, drain queuing for the synthesized usage event,
// and attempt success recording. Mandatory encoder/evidence preflight competes
// before NormalFinish settlement so encoder failure can own the terminal.
func (s *retryRecvStream) handleResponseFinishedPath(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) (lipapi.Event, bool, error) {
	attempt := s.attempt.require()
	recording := s.responsePipeline.recordClientFacing(ctx, s.facts, attempt, s.executor, ev, s.isCommitted())
	if recording.mandatory() {
		s.responsePipeline.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
		s.terminalizePartialFailure(ctx, sdkterminal.CommandFrontendEncoderFailure, attemptReasonDetail(recording.err), recording.err)
		return lipapi.Event{}, false, recording.err
	}
	if recording.err != nil && s.executor != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", recording.err)
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
		s.prependRecoveryDrain(ev)
		ev, err := s.emitSynthesizedUsage(ctx, usageEv)
		return ev, false, err
	}
	s.executor.recordAttemptLogged(ctx, recordAttemptParams{
		ALegID:  s.facts.aLegID,
		BLeg:    attempt.bleg,
		Cand:    attempt.cand,
		Outcome: lipapi.AttemptSuccess,
	}, diag.AttrOpts{CallID: s.facts.traceID, BLegID: attempt.bleg.BLegID})
	s.responsePipeline.commitSuccessfulTurn(s.facts, attempt, s.executor.Keepwarm, s.isCommitted())
	s.markFinished()
	s.terminal.endALeg(aLegEndBase)
	out, _, obsErr := s.responsePipeline.observeClientFacing(ctx, ev, responseEventInput{
		facts: s.facts, executor: s.executor, attempt: attempt, recovery: s.recovery,
		pm: pm, committed: s.isCommitted(), now: s.now(), recorded: true, finishAfterRemember: true,
	})
	if obsErr != nil {
		s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(obsErr), obsErr)
		return lipapi.Event{}, false, obsErr
	}
	return out, false, nil
}

// terminalizePartialFailure routes mid-stream / finish-adjacent failures through the
// request terminal so settle/release/facts run once under the owner.
func (s *retryRecvStream) terminalizePartialFailure(ctx context.Context, cmd sdkterminal.Command, reason string, cause error) {
	attempt := s.attempt.require()
	_ = s.terminal.terminalizeSnapshot(ctx, cmd, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		if !attempt.authority.Settled() {
			s.recordPartialTokenAccounting(cctx, attempt, reason, cause)
		}
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
}

func (s *retryRecvStream) handleRecvEOF(ctx context.Context) (lipapi.Event, error) {
	attempt := s.attempt.require()
	clearAttemptToolState(s.responsePipeline, s.attempt.snapshot())
	// Truncated upstream: never run completion gates on a partial buffer (replace gates could
	// synthesize response_finished and mask the failure).
	gates := s.completionGatesFromContext(ctx)
	if len(gates) > 0 {
		s.abandonIncompleteGateBuffer()
	}
	if s.recovery != nil && s.recovery.recoverPolicy != nil {
		dec := s.recovery.recoverPolicy.DecideEOF(io.EOF, s.now())
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
				s.appendRecoveryDrain(dec.Warning)
			}
			s.appendRecoveryDrain(dec.Finish)
			ev, _ := s.popRecoveryDrain()
			if ev.Kind == lipapi.EventResponseFinished {
				s.prependRecoveryDrain(ev)
				return lipapi.Event{}, nil
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
			return out, emitErr
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
	s.terminal.terminalizeSnapshot(ctx, sdkterminal.CommandEOF, attempt, s.accumulatorSnapshot(), func(cctx context.Context, _ coreterm.Outcome) error {
		s.recordPartialTokenAccounting(cctx, attempt, "stream ended without response_finished", io.EOF)
		s.responsePipeline.finishFinalStreamObservation(cctx, attempt, response.OutcomeFailed)
		s.markFinished()
		return nil
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		s.recordBillingLegForAttempt(cctx, attempt, sdkterminal.CommandEOF)
		s.terminal.handoffBillingTurn(cctx, s.facts, s.executor, sdkterminal.CommandEOF)
		return nil
	})
	if !s.isFinished() {
		s.markFinished()
	}
	s.terminal.endALeg(aLegEndBase)
	return lipapi.Event{}, io.EOF
}
