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
)

// handleRecvSuccess is the per-event success path dispatched by Recv. It runs
// the per-event accounting, traffic, and hook setup shared by every branch,
// then fans out to one of three local helpers: the tool-event path (which
// may short-circuit on reactor swallow or policy error), the completion-gates
// path, or the no-gates response_finished path. The no-gates non-finished
// default falls through inline because it shares its observation and emit
// steps with the response_finished path.
func (s *retryRecvStream) handleRecvSuccess(ctx context.Context, ev lipapi.Event) (lipapi.Event, bool, error) {
	recvAt := s.now()
	s.accounting.observeBackendEvent(recvAt, ev)
	s.accounting.observeUsage(ev)
	pm, tm := s.recvHookMeta()
	s.emitTrafficBTP(ctx, ev, pm)
	ev = s.enrichUsageCost(ev)
	s.emitUsage(ctx, ev)

	// Tool-event path may short-circuit with a swallow or an error before the
	// rest of the dispatch runs.
	if te, ok := lipapi.ToolEventFromEvent(ev); ok {
		nextEv, swallowed, err := s.handleToolEventPath(ctx, te, ev, tm)
		if err != nil || swallowed {
			return lipapi.Event{}, swallowed, err
		}
		ev = nextEv
	}

	evp := ev
	if herr := s.bus.RunResponsePartHooks(ctx, &evp, pm); herr != nil {
		s.recordPartialTokenAccounting(ctx, attemptReasonDetail(herr), herr)
		return lipapi.Event{}, false, herr
	}
	ev = evp

	if gates := s.completionGatesFromContext(ctx); len(gates) > 0 {
		return s.handleGatedPath(ctx, gates, ev, pm)
	}

	// No-gates branch: observe the client event, then dispatch to the
	// response_finished helper or fall through to the default client-event emit.
	if lipapi.OutputCommitted(ev) {
		s.markOutputCommitted(ev)
	}
	s.accounting.observeClientEvent(s.now(), ev)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(ev, s.now())
	}
	s.rememberClientEvent(ev)
	if ev.Kind == lipapi.EventResponseFinished {
		return s.handleResponseFinishedPath(ctx, ev, pm)
	}
	if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			if !s.authority.Settled() {
				s.recordPartialTokenAccounting(ctx, attemptReasonDetail(err), err)
			}
			return lipapi.Event{}, false, err
		}
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
	}
	s.commitAffinityIfOutput(ctx, ev)
	s.emitTrafficPTC(ctx, ev, pm)
	return ev, false, nil
}

// handleToolEventPath runs tool policies, tool reactors, and merges a
// replacement event back into the recv event when the reactor produces one.
// It returns the (possibly merged) event to continue dispatch with, a
// swallowed bool when the reactor asked to drop the event, or a non-nil
// error from policy or reactor execution.
func (s *retryRecvStream) handleToolEventPath(ctx context.Context, te lipapi.ToolEvent, ev lipapi.Event, tm sdk.ToolMeta) (lipapi.Event, bool, error) {
	if err := s.applyToolPolicies(ctx, te, tm); err != nil {
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.aLegID,
			BLeg:      s.bleg,
			Cand:      s.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    attemptReasonDetail(err),
			DetailErr: err,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		s.recordPartialTokenAccounting(ctx, attemptReasonDetail(err), err)
		return lipapi.Event{}, false, err
	}
	res := s.bus.ApplyToolReactors(ctx, te, tm)
	if res.Err != nil {
		s.recordPartialTokenAccounting(ctx, attemptReasonDetail(res.Err), res.Err)
		return lipapi.Event{}, false, res.Err
	}
	if !res.Emit {
		return lipapi.Event{}, true, nil
	}
	if res.Event.Kind != "" {
		ev = lipapi.MergeToolEventInto(ev, res.Event)
	}
	return ev, false, nil
}

// handleGatedPath runs the completion-gates buffer-and-replace pipeline, then
// observes and emits the drained event. It owns the per-event traffic
// emission for the gated branch.
func (s *retryRecvStream) handleGatedPath(ctx context.Context, gates []completion.Gate, ev lipapi.Event, pm sdk.PartMeta) (lipapi.Event, bool, error) {
	out, gerr := s.completionGatedEmit(ctx, gates, ev)
	if errors.Is(gerr, errGateContinueInner) {
		return lipapi.Event{}, true, nil
	}
	if gerr != nil {
		s.recordPartialTokenAccounting(ctx, attemptReasonDetail(gerr), gerr)
		return lipapi.Event{}, false, gerr
	}
	// A gated completion that drains a response_finished finalizes authority through the single
	// finalizeResponseFinishedAuthority chokepoint (settle, with a losing release fallback when the
	// settle did not mark the reservation settled), matching the no-gates handleResponseFinishedPath.
	// Without this the reservation stays locked until the accounting window resets.
	if out.Kind == lipapi.EventResponseFinished && !s.tokenAccountingFinalized {
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
	s.markOutputCommitted(out)
	s.accounting.observeClientEvent(s.now(), out)
	if s.recoverPolicy != nil {
		s.recoverPolicy.ObserveClientEvent(out, s.now())
	}
	if err := s.beforeEmitClientFacing(ctx, out); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			if !s.authority.Settled() {
				s.recordPartialTokenAccounting(ctx, attemptReasonDetail(err), err)
			}
			return lipapi.Event{}, false, err
		}
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
	}
	s.commitAffinityIfOutput(ctx, out)
	s.emitTrafficPTC(ctx, out, pm)
	return out, false, nil
}

// handleResponseFinishedPath finalizes the response_finished branch: token
// accounting finalization, drain queuing for the synthesized usage event,
// and attempt success recording. The non-synthesized path falls through to
// the same default client-event emit used by the dispatcher's non-finished
// branch.
func (s *retryRecvStream) handleResponseFinishedPath(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) (lipapi.Event, bool, error) {
	usageEv, ok, err := s.finalizeResponseFinishedAuthority(ctx, ev)
	if err != nil {
		return lipapi.Event{}, false, err
	}
	if ok {
		s.recoverDrain = append([]lipapi.Event{ev}, s.recoverDrain...)
		ev, err := s.emitSynthesizedUsage(ctx, usageEv)
		return ev, false, err
	}
	s.executor.recordAttemptLogged(ctx, recordAttemptParams{
		ALegID:  s.aLegID,
		BLeg:    s.bleg,
		Cand:    s.cand,
		Outcome: lipapi.AttemptSuccess,
	}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
	s.markFinished()
	s.finishALegScope()
	if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			if !s.authority.Settled() {
				s.recordPartialTokenAccounting(ctx, attemptReasonDetail(err), err)
			}
			return lipapi.Event{}, false, err
		}
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
	}
	s.commitAffinityIfOutput(ctx, ev)
	s.emitTrafficPTC(ctx, ev, pm)
	return ev, false, nil
}

func (s *retryRecvStream) handleRecvEOF(ctx context.Context) (lipapi.Event, error) {
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
				ALegID:    s.aLegID,
				BLeg:      s.bleg,
				Cand:      s.cand,
				Outcome:   lipapi.AttemptSuccess,
				Reason:    dec.Reason,
				DetailErr: io.EOF,
			}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
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
			return ev, nil
		}
	}
	s.recordPartialTokenAccounting(ctx, "stream ended without response_finished", io.EOF)
	if !s.isFinished() {
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:    s.aLegID,
			BLeg:      s.bleg,
			Cand:      s.cand,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    "stream ended without response_finished",
			DetailErr: io.EOF,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
	}
	s.markFinished()
	s.finishALegScope()
	return lipapi.Event{}, io.EOF
}
