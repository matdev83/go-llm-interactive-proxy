package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func (s *retryRecvStream) streamObserverMeta(ctx context.Context) response.StreamMeta {
	attempt := s.attempt.require()
	backendID := strings.TrimSpace(attempt.cand.Primary.Backend)
	var prefixes []string
	if s.executor != nil {
		if be, ok := s.executor.Backends[backendID]; ok {
			prefixes = execbackend.CloneBackendPrefixes(be)
		}
	}
	meta := response.StreamMeta{
		TraceID: s.facts.traceID, ALegID: s.facts.aLegID, BLegID: attempt.bleg.BLegID, CandidateKey: attempt.cand.Key,
		BackendID: backendID, BackendPrefixes: prefixes, Model: strings.TrimSpace(attempt.cand.Primary.Model),
		AttemptSeq: attempt.bleg.Seq,
	}
	if v, ok := s.viewsFor(ctx); ok {
		meta.Scope = v.Scope.Clone()
		meta.Session, meta.Workspace = cloneSessionView(v.Session), cloneWorkspaceView(v.Workspace)
		meta.Session.ALegID = s.facts.aLegID
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
			s.terminalizePartialFailure(ctx, sdkterminal.CommandPartialError, attemptReasonDetail(err), err)
			if !s.isCommitted() {
				s.finishALegScope()
			}
			return lipapi.Event{}, err
		}
	}
	if lipapi.OutputCommitted(ev) {
		s.markOutputCommitted(ev)
	}
	if err := s.beforeEmitClientFacing(ctx, ev); err != nil {
		if s.executor != nil && s.executor.SecureSessionRecordingMandatory {
			s.finishFinalStreamObservation(ctx, response.OutcomeFailed)
			s.terminalizePartialFailure(ctx, sdkterminal.CommandFrontendEncoderFailure, attemptReasonDetail(err), err)
			if !s.isCommitted() {
				s.finishALegScope()
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
	// Compaction preservation and committed detector observation run before the
	// event is remembered or returned, so customer evidence and the client see
	// the same finalized canonical event.
	releaseDispatch := s.emitTrafficPTCFinal(ctx, &ev, pm)
	// Remember only after mandatory recording and compaction finalization succeed
	// (or are best-effort), so undelivered client output is not settled into
	// customer evidence.
	s.rememberClientEvent(ev)
	s.commitAffinityIfOutput(ctx, ev)
	s.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, nil
}
