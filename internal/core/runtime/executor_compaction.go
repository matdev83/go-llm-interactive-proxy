package runtime

import (
	"context"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

type compactionReleaseDispatch struct {
	meta    compaction.PreservationMeta
	enabled bool
}

// compactionObservers returns the frozen compaction observer slice bound to
// this executor's runtime snapshot. A nil snapshot yields nil (no-op).
func (e *Executor) compactionObservers() []compaction.Observer {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.CompactionObservers()
}

func (e *Executor) compactionPreservers() []compaction.Preserver {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.CompactionPreservers()
}

func (e *Executor) compactionServices() compaction.Services {
	services := compaction.Services{}
	if e != nil && e.RuntimeSnapshot != nil {
		services.State = e.RuntimeSnapshot.State()
	}
	if e != nil {
		services.BackgroundAux = e.BackgroundAux
	}
	return services
}

// observeCompactionOpened runs the request-side compaction observation after
// the first upstream B-leg opened successfully for a logical request. Retry/
// failover replacement B-legs never reach this point, so starts and
// fingerprints are recorded exactly once per logical request (requirements
// 3.2, 3.5, 4.6). Observation is fail-open and never alters execution. The
// process-owned detector remains authoritative independently of metadata
// observer registration so later preservation consumers can use its state and
// pure previews.
func (e *Executor) observeCompactionOpened(ctx context.Context, prep *preparedRequest, out attemptOpenResult) compaction.PreservationMeta {
	if e == nil || e.Detector == nil || prep == nil {
		return compaction.PreservationMeta{}
	}
	observers := e.compactionObservers()
	meta := compactiondetect.RequestMeta{
		TraceID:    prep.traceID,
		ALegID:     prep.aLeg.ALegID,
		BLegID:     out.bleg.BLegID,
		AttemptSeq: out.bleg.Seq,
		SessionID:  prep.baseline.Session.AuthoritativeSessionID,
	}
	events := safeCompactionRequestOpened(e.Detector, meta, prep.baseline)
	preservationMeta := compaction.PreservationMeta{
		TraceID:    meta.TraceID,
		SessionID:  meta.SessionID,
		ALegID:     meta.ALegID,
		BLegID:     meta.BLegID,
		AttemptSeq: meta.AttemptSeq,
	}
	if len(events) > 0 {
		last := events[len(events)-1]
		preservationMeta.TransactionID = last.TransactionID
		preservationMeta.RuleID = string(last.RuleID)
		preservationMeta.Evidence = last.Evidence
	}
	// The detector commits before content-bearing callbacks. RequestOpened gets
	// isolated callback-local copies because the primary request is already on
	// the wire and cannot be rolled back. Public metadata dispatch is last.
	_ = extensions.RunCompactionPreserverRequestOpened(
		ctx,
		e.Log,
		e.ExtensionMetrics,
		e.compactionPreservers(),
		prep.baseline,
		events,
		preservationMeta,
		e.compactionServices(),
	)
	compaction.Dispatch(ctx, observers, events)
	return preservationMeta
}

func (e *Executor) notifyCompactionOpenFailed(ctx context.Context, prep *preparedRequest) {
	if e == nil || prep == nil {
		return
	}
	meta := compaction.PreservationMeta{
		TraceID:   prep.traceID,
		SessionID: prep.baseline.Session.AuthoritativeSessionID,
		ALegID:    prep.aLeg.ALegID,
	}
	_ = extensions.RunCompactionPreserverRequestOpenFailed(
		ctx,
		e.Log,
		e.ExtensionMetrics,
		e.compactionPreservers(),
		meta,
		e.compactionServices(),
	)
}

// observeCompactionRelease runs the response-side compaction observation for
// every canonical event actually released by the retry stream. It is called
// from the single final release seam (emitTrafficPTC), so live, gated,
// tool-finalizer, and recovery drains are observed exactly once and the
// returned event is never altered (requirements 3.3, 8.4). The detector is
// committed even when no metadata observers are configured; dispatch is only
// the optional public side effect.
func (s *retryRecvStream) observeCompactionRelease(ctx context.Context, ev lipapi.Event) {
	s.observeCompactionReleaseFinal(ctx, &ev)
}

func (s *retryRecvStream) observeCompactionReleaseFinal(ctx context.Context, ev *lipapi.Event) compactionReleaseDispatch {
	var dispatch compactionReleaseDispatch
	if s == nil || s.executor == nil || s.executor.Detector == nil {
		return dispatch
	}
	attempt := s.attempt.require()
	observers := s.executor.compactionObservers()
	meta := compactiondetect.ResponseMeta{
		TraceID:    s.facts.traceID,
		ALegID:     s.facts.aLegID,
		BLegID:     attempt.bleg.BLegID,
		AttemptSeq: attempt.bleg.Seq,
		SessionID:  s.facts.baseline.Session.AuthoritativeSessionID,
	}
	preview := safeCompactionPreviewResponse(s.executor.Detector, meta, *ev)
	preservationMeta := compaction.PreservationMeta{
		TraceID:       meta.TraceID,
		SessionID:     meta.SessionID,
		ALegID:        meta.ALegID,
		BLegID:        meta.BLegID,
		AttemptSeq:    meta.AttemptSeq,
		TransactionID: preview.TransactionID,
		RuleID:        preview.RuleID,
		Evidence:      preview.Evidence,
	}
	// Completion-only requests can have their committed transaction established
	// by RequestOpened while the later ordinary response has an empty pure
	// preview. Preserve response correlation and use only the request-side
	// transaction/rule/evidence as a fallback in that case.
	if strings.TrimSpace(preservationMeta.TransactionID) == "" {
		fallback := s.compactionOpenMeta
		preservationMeta.TransactionID = fallback.TransactionID
		if preservationMeta.RuleID == "" {
			preservationMeta.RuleID = fallback.RuleID
		}
		if preservationMeta.Evidence == "" {
			preservationMeta.Evidence = fallback.Evidence
		}
	}
	// Pure preview is deliberately before preservation. The callback runner
	// rolls back each failed/panicking/invalid mutation before committed detector
	// observation, so detector and client receive the same final event.
	_ = extensions.RunCompactionPreserverBeforeResponseRelease(
		ctx,
		s.executor.Log,
		s.executor.ExtensionMetrics,
		s.executor.compactionPreservers(),
		ev,
		preview,
		preservationMeta,
		s.executor.compactionServices(),
	)
	events := safeCompactionResponseReleased(s.executor.Detector, meta, *ev)
	dispatch = compactionReleaseDispatch{meta: preservationMeta, enabled: true}
	if len(events) == 0 {
		return dispatch
	}
	compaction.Dispatch(ctx, observers, events)
	return dispatch
}

func (s *retryRecvStream) notifyCompactionAfterRelease(ctx context.Context, ev lipapi.Event, dispatch compactionReleaseDispatch) {
	if s == nil || s.executor == nil || !dispatch.enabled {
		return
	}
	_ = extensions.RunCompactionPreserverAfterResponseRelease(
		ctx,
		s.executor.Log,
		s.executor.ExtensionMetrics,
		s.executor.compactionPreservers(),
		ev,
		dispatch.meta,
		s.executor.compactionServices(),
	)
}

func safeCompactionRequestOpened(d *compactiondetect.Detector, meta compactiondetect.RequestMeta, call lipapi.Call) (events []compaction.Event) {
	defer func() {
		if recover() != nil {
			events = nil
		}
	}()
	return d.RequestOpened(meta, call)
}

func safeCompactionResponseReleased(d *compactiondetect.Detector, meta compactiondetect.ResponseMeta, ev lipapi.Event) (events []compaction.Event) {
	defer func() {
		if recover() != nil {
			events = nil
		}
	}()
	return d.ResponseReleased(meta, ev)
}

func safeCompactionPreviewResponse(d *compactiondetect.Detector, meta compactiondetect.ResponseMeta, ev lipapi.Event) (preview compaction.ResponsePreview) {
	defer func() {
		if recover() != nil {
			preview = compaction.ResponsePreview{}
		}
	}()
	return d.PreviewResponse(meta, ev)
}
