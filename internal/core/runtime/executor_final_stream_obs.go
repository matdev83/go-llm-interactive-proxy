package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

func (s *retryRecvStream) streamObserverMeta(ctx context.Context) response.StreamMeta {
	meta := response.StreamMeta{
		TraceID: s.traceID, ALegID: s.aLegID, BLegID: s.bleg.BLegID, CandidateKey: s.cand.Key,
		BackendID: strings.TrimSpace(s.cand.Primary.Backend), Model: strings.TrimSpace(s.cand.Primary.Model),
		AttemptSeq: s.bleg.Seq,
	}
	if v, ok := s.viewsFor(ctx); ok {
		meta.Scope = v.Scope.Clone()
		meta.Session, meta.Workspace = cloneSessionView(v.Session), cloneWorkspaceView(v.Workspace)
		meta.Session.ALegID = s.aLegID
	}
	return meta
}

func (s *retryRecvStream) openFinalStreamObservation(ctx context.Context) error {
	if s == nil || s.executor == nil || s.executor.RuntimeSnapshot == nil {
		return nil
	}
	factories := s.executor.RuntimeSnapshot.StreamObserverFactories()
	if len(factories) == 0 {
		return nil
	}
	if s.finalStreamObs == nil {
		s.finalStreamObs = &extensions.FinalStreamObservationSession{Log: s.executor.Log, Metrics: s.executor.ExtensionMetrics}
	}
	if err := s.finalStreamObs.Open(ctx, factories, s.streamObserverMeta(ctx), response.Services{}); err != nil && !s.isCommitted() {
		return err
	}
	return nil
}

func (s *retryRecvStream) finishFinalStreamObservation(ctx context.Context, outcome response.StreamOutcome) {
	if s != nil && s.finalStreamObs != nil {
		s.finalStreamObs.Finish(ctx, outcome)
	}
}

func (s *retryRecvStream) cycleFinalStreamObservation(ctx context.Context, outcome response.StreamOutcome) error {
	s.finishFinalStreamObservation(ctx, outcome)
	return s.openFinalStreamObservation(ctx)
}

func (s *retryRecvStream) emitClientFacingObserved(ctx context.Context, ev lipapi.Event, pm sdk.PartMeta) (lipapi.Event, error) {
	if s.executor != nil {
		if err := extensions.RunFinalStreamObservationStage(ctx, s.executor.Log, s.executor.ExtensionMetrics, s.finalStreamObs, ev, s.isCommitted()); err != nil {
			s.finishFinalStreamObservation(ctx, response.OutcomeFailed)
			if !s.isCommitted() {
				if !s.authority.Settled() {
					s.recordPartialTokenAccounting(ctx, attemptReasonDetail(err), err)
				}
				s.finishALegScope()
			}
			s.markFinished()
			return lipapi.Event{}, err
		}
	}
	if lipapi.OutputCommitted(ev) {
		s.markOutputCommitted(ev)
	}
	if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			s.finishFinalStreamObservation(ctx, response.OutcomeFailed)
			if !s.authority.Settled() {
				s.recordPartialTokenAccounting(ctx, attemptReasonDetail(err), err)
			}
			return lipapi.Event{}, err
		}
		if s.executor != nil && s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "secure_session recorder stream", "error", err)
		}
	}
	if ev.Kind == lipapi.EventResponseFinished {
		s.finishFinalStreamObservation(ctx, response.OutcomeSuccessReleased)
	}
	s.commitAffinityIfOutput(ctx, ev)
	s.emitTrafficPTC(ctx, ev, pm)
	return ev, nil
}
