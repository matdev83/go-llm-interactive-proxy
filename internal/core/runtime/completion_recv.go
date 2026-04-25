package runtime

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
)

// errGateContinueInner signals Recv to pull another inner event without returning to the client yet.
var errGateContinueInner = errors.New("runtime: completion gate continue buffering")

func gateBufHasCommittedOutput(buf []lipapi.Event) bool {
	return slices.ContainsFunc(buf, lipapi.OutputCommitted)
}

func (s *retryRecvStream) completionSnapshot(ctx context.Context) *extensions.RequestRuntimeSnapshot {
	if snap := extensions.RequestRuntimeSnapshotFromContext(ctx); snap != nil {
		return snap
	}
	if s.executor != nil {
		return s.executor.RuntimeSnapshot
	}
	return nil
}

func (s *retryRecvStream) completionGatesFromContext(ctx context.Context) []completion.Gate {
	var fallback extensions.CompletionGatesView
	if s.executor != nil {
		fallback = s.executor.RuntimeSnapshot
	}
	return extensions.CompletionGatesFromContext(ctx, fallback)
}

func (s *retryRecvStream) popGateDrainHead() (lipapi.Event, bool) {
	if len(s.gateDrain) == 0 {
		return lipapi.Event{}, false
	}
	ev := s.gateDrain[0]
	s.gateDrain = s.gateDrain[1:]
	return ev, true
}

func (s *retryRecvStream) emitGateDrained(ctx context.Context, ev lipapi.Event) lipapi.Event {
	if lipapi.OutputCommitted(ev) {
		s.committed = true
	}
	if ev.Kind == lipapi.EventResponseFinished {
		s.executor.recordAttemptLogged(ctx, recordAttemptParams{
			ALegID:  s.aLegID,
			BLeg:    s.bleg,
			Cand:    s.cand,
			Outcome: lipapi.AttemptSuccess,
		}, diag.AttrOpts{CallID: s.traceID, BLegID: s.bleg.BLegID})
		s.finished = true
	}
	return ev
}

func (s *retryRecvStream) completionGatedEmit(
	ctx context.Context,
	gates []completion.Gate,
	ev lipapi.Event,
) (lipapi.Event, error) {
	if s.gateLive {
		return ev, nil
	}
	limits := completion.DefaultBufferLimits()
	if s.executor != nil && s.executor.CompletionBufferLimits.MaxEvents > 0 {
		limits = s.executor.CompletionBufferLimits
	}
	if len(s.gateBuf) == 0 {
		maxEv := limits.MaxEvents
		if maxEv <= 0 {
			maxEv = completion.DefaultBufferLimits().MaxEvents
		}
		const prealloc = 64
		capN := min(prealloc, maxEv)
		s.gateBuf = make([]lipapi.Event, 0, capN)
	}
	s.gateBuf = append(s.gateBuf, ev)
	if extensions.CompletionGateBufferExceeded(limits, len(s.gateBuf)) {
		s.gateLive = true
		s.gateDrain = slices.Clone(s.gateBuf)
		s.gateBuf = nil
		if len(s.gateDrain) == 0 {
			return lipapi.Event{}, errors.New("runtime: completion gate overflow with empty buffer")
		}
		first := s.gateDrain[0]
		s.gateDrain = s.gateDrain[1:]
		return first, nil
	}
	if ev.Kind == lipapi.EventResponseFinished {
		snap := s.completionSnapshot(ctx)
		meta := completion.Meta{
			TraceID:    s.traceID,
			ALegID:     s.aLegID,
			BLegID:     s.bleg.BLegID,
			AttemptSeq: s.bleg.Seq,
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
		committedForPanic := s.committed || gateBufHasCommittedOutput(s.gateBuf)
		out, err := safety.CallValue(safety.BoundaryStream, "completion_gate_chain", func() ([]lipapi.Event, error) {
			return extensions.ApplyCompletionGateChain(ctx, gates, meta, s.gateBuf, s.committed, svc, stageLog)
		})
		if err != nil {
			var pe *safety.PanicError
			if errors.As(err, &pe) {
				s.gateBuf = nil
				s.gateDrain = nil
				s.gateLive = false
				return lipapi.Event{}, mapStreamPanic(pe, committedForPanic)
			}
			s.gateBuf = nil
			return lipapi.Event{}, err
		}
		s.gateBuf = nil
		if len(out) == 0 {
			return lipapi.Event{}, errors.New("runtime: completion gate produced empty stream")
		}
		s.gateDrain = out[1:]
		return out[0], nil
	}
	return lipapi.Event{}, errGateContinueInner
}
