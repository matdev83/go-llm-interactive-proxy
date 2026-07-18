package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const finalStreamCleanupTimeout = 5 * time.Second

type finalStreamObserverSlot struct {
	id   string
	mode sdkhooks.FailureMode
	obs  response.StreamObserver
}

type FinalStreamObservationSession struct {
	mu                                            sync.Mutex
	cond                                          *sync.Cond
	Log                                           *slog.Logger
	Metrics                                       StageMetrics
	slots                                         []finalStreamObserverSlot
	finished, opening, pendingFinish, hasDeferred bool
	observeInFlight                               int
	pendingOutcome, deferredOutcome               response.StreamOutcome
	deferredSlots                                 []finalStreamObserverSlot
}

func (s *FinalStreamObservationSession) condLocked() *sync.Cond {
	if s.cond == nil {
		s.cond = sync.NewCond(&s.mu)
	}
	return s.cond
}

func finishStreamObserverSlots(parentCtx context.Context, log *slog.Logger, metrics StageMetrics, slots []finalStreamObserverSlot, outcome response.StreamOutcome) {
	if len(slots) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), finalStreamCleanupTimeout)
	defer cancel()
	for _, slot := range slots {
		res := runBoundedProvider(ctx, nil, feature.StageIDFinalStreamObservation, slot.id, func(c context.Context) (struct{}, error) {
			return struct{}{}, safety.Call(safety.BoundaryExtension, "final_stream_finish", func() error {
				return slot.obs.Finish(c, outcome)
			})
		})
		isolateFinalStreamErr(ctx, log, metrics, slot.id, res.Err, "final_stream_finish")
	}
}

func (s *FinalStreamObservationSession) takeDeferredLocked() ([]finalStreamObserverSlot, response.StreamOutcome, bool) {
	if !s.hasDeferred || s.observeInFlight > 0 {
		return nil, "", false
	}
	slots, outcome := s.deferredSlots, s.deferredOutcome
	s.hasDeferred, s.deferredSlots = false, nil
	return slots, outcome, true
}

func (s *FinalStreamObservationSession) Open(ctx context.Context, factories []response.StreamObserverFactory, meta response.StreamMeta, svc response.Services) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	s.mu.Lock()
	for s.opening {
		s.condLocked().Wait()
	}
	if s.observeInFlight > 0 {
		s.mu.Unlock()
		return fmt.Errorf("extensions: final stream Open during Observe")
	}
	s.opening = true
	var prior []finalStreamObserverSlot
	var priorOutcome response.StreamOutcome
	if slots, outcome, ok := s.takeDeferredLocked(); ok {
		prior, priorOutcome = slots, outcome
	} else if !s.finished && len(s.slots) > 0 {
		prior, priorOutcome = slices.Clone(s.slots), response.OutcomeReplaced
	}
	s.slots, s.finished, s.pendingFinish, s.pendingOutcome = nil, false, false, ""
	s.hasDeferred, s.deferredSlots = false, nil
	log, metrics := s.Log, s.Metrics
	s.mu.Unlock()
	finishStreamObserverSlots(ctx, log, metrics, prior, priorOutcome)

	var opened []finalStreamObserverSlot
	var openErr error
	for _, f := range response.MaterializeSorted(factories) {
		if f == nil || execctx.IsSuppressedPluginID(ctx, f.ID()) {
			continue
		}
		mode := f.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		obs, oerr := safety.CallValue(safety.BoundaryExtension, "final_stream_open", func() (response.StreamObserver, error) {
			return f.Open(ctx, meta, svc)
		})
		if oerr != nil {
			isolateFinalStreamErr(ctx, s.Log, s.Metrics, f.ID(), oerr, "final_stream_open")
			if mode == sdkhooks.FailClosed {
				openErr = PolicyErrorFromProviderFailure(feature.StageIDFinalStreamObservation, f.ID(), failureBehaviorFromMode(mode), oerr)
				break
			}
			continue
		}
		if obs != nil {
			opened = append(opened, finalStreamObserverSlot{id: f.ID(), mode: mode, obs: obs})
		}
	}

	s.mu.Lock()
	finishOpened := openErr != nil || s.pendingFinish
	outcome := response.OutcomeFailed
	if s.pendingFinish {
		outcome = s.pendingOutcome
	}
	s.pendingFinish = false
	if finishOpened {
		s.finished, s.slots, s.opening = true, nil, false
		log, metrics = s.Log, s.Metrics
		s.condLocked().Broadcast()
		s.mu.Unlock()
		finishStreamObserverSlots(ctx, log, metrics, opened, outcome)
		return openErr
	}
	s.slots, s.opening = opened, false
	s.condLocked().Broadcast()
	s.mu.Unlock()
	return nil
}

func (s *FinalStreamObservationSession) Finish(parentCtx context.Context, outcome response.StreamOutcome) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.opening {
		if !s.pendingFinish {
			s.pendingFinish, s.pendingOutcome = true, outcome
		}
		s.mu.Unlock()
		return
	}
	if s.finished || len(s.slots) == 0 {
		s.finished = true
		s.mu.Unlock()
		return
	}
	s.finished = true
	run := slices.Clone(s.slots)
	s.slots = nil
	if s.observeInFlight > 0 {
		s.hasDeferred, s.deferredSlots, s.deferredOutcome = true, run, outcome
		s.mu.Unlock()
		return
	}
	log, metrics := s.Log, s.Metrics
	s.mu.Unlock()
	finishStreamObserverSlots(parentCtx, log, metrics, run, outcome)
}

func RunFinalStreamObservationStage(ctx context.Context, log *slog.Logger, obs StageMetrics, session *FinalStreamObservationSession, ev lipapi.Event, committed bool) error {
	if session == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	session.mu.Lock()
	if session.finished || session.opening || len(session.slots) == 0 {
		session.mu.Unlock()
		return nil
	}
	slots := slices.Clone(session.slots)
	session.observeInFlight++
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.observeInFlight--
		flush, flushOutcome, ok := session.takeDeferredLocked()
		flog, fmetrics := session.Log, session.Metrics
		if session.observeInFlight == 0 {
			session.condLocked().Broadcast()
		}
		session.mu.Unlock()
		if ok {
			finishStreamObserverSlots(ctx, flog, fmetrics, flush, flushOutcome)
		}
	}()

	start, stageOutcome := time.Now(), "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "lip.extension.final_stream_observe")
	defer func() {
		RecordStageObservation(obs, MetricsStageFinalStreamObservation, stageOutcome, time.Since(start).Seconds(), 1, SafeEventObserveBytes(ev))
		if stageOutcome == "error" {
			span.SetStatus(codes.Error, "observe error")
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()
	ev.UsageScopes = slices.Clone(ev.UsageScopes)
	evidence := DecisionEvidenceFromContext(ctx)
	for _, slot := range slots {
		res := runBoundedProvider(ctx, evidence, feature.StageIDFinalStreamObservation, slot.id, func(c context.Context) (struct{}, error) {
			return struct{}{}, safety.Call(safety.BoundaryExtension, "final_stream_observe", func() error {
				return slot.obs.Observe(c, ev)
			})
		})
		if res.ParentCanceled {
			if committed {
				isolateFinalStreamErr(ctx, log, obs, slot.id, res.Err, "final_stream_observe")
				continue
			}
			stageOutcome = "error"
			return res.Err
		}
		if res.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, evidence, finalStreamObserveFailureCfg, res.IterCtx, slot.id, res.Deadline, slot.mode)
			if cont {
				continue
			}
			if committed {
				isolateFinalStreamErr(ctx, log, obs, slot.id, terr, "final_stream_observe")
				continue
			}
			stageOutcome = "error"
			return terr
		}
		if res.Err != nil {
			if committed || slot.mode == sdkhooks.FailOpen {
				isolateFinalStreamErr(ctx, log, obs, slot.id, res.Err, "final_stream_observe")
				continue
			}
			stageOutcome = "error"
			return PolicyErrorFromProviderFailure(feature.StageIDFinalStreamObservation, slot.id, failureBehaviorFromMode(slot.mode), res.Err)
		}
	}
	return nil
}

func isolateFinalStreamErr(ctx context.Context, log *slog.Logger, obs StageMetrics, id string, err error, phase string) {
	if err == nil {
		return
	}
	var pe *safety.PanicError
	if errors.As(err, &pe) {
		logFailOpenExtensionPanic(ctx, log, phase, id, err)
	} else if log != nil {
		log.WarnContext(ctx, phase+": observer error (isolated)", "factory", id, "error", err)
	}
	if obs != nil {
		obs.IncFailOpenSkip(MetricsStageFinalStreamObservation)
	}
}

var finalStreamObserveFailureCfg = stageFailureConfig{
	Stage: feature.StageIDFinalStreamObservation, MetricsStage: MetricsStageFinalStreamObservation,
	TimeoutMsg: "final_stream_observe: handler timed out (fail-open)", FailureMsg: "final_stream_observe: handler error (fail-open)",
	PanicStage: "final_stream_observe", ProviderAttr: "factory",
}
