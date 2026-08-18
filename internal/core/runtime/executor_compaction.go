package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
)

// compactionObservers returns the frozen compaction observer slice bound to
// this executor's runtime snapshot. A nil snapshot yields nil (no-op).
func (e *Executor) compactionObservers() []compaction.Observer {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.CompactionObservers()
}

// observeCompactionOpened runs the request-side compaction observation after
// the first upstream B-leg opened successfully for a logical request. Retry/
// failover replacement B-legs never reach this point, so starts and
// fingerprints are recorded exactly once per logical request (requirements
// 3.2, 3.5, 4.6). Observation is fail-open and never alters execution. The
// process-owned detector remains authoritative independently of metadata
// observer registration so later preservation consumers can use its state and
// pure previews.
func (e *Executor) observeCompactionOpened(ctx context.Context, prep *preparedRequest, out attemptOpenResult) {
	if e == nil || e.CompactionRuntime.Detector == nil || prep == nil {
		return
	}
	observers := e.compactionObservers()
	meta := compactiondetect.RequestMeta{
		TraceID:    prep.traceID,
		ALegID:     prep.aLeg.ALegID,
		BLegID:     out.bleg.BLegID,
		AttemptSeq: out.bleg.Seq,
		SessionID:  prep.baseline.Session.AuthoritativeSessionID,
	}
	events := e.CompactionRuntime.Detector.RequestOpened(meta, prep.baseline)
	if len(events) == 0 {
		return
	}
	compaction.Dispatch(ctx, observers, events)
}

// observeCompactionRelease runs the response-side compaction observation for
// every canonical event actually released by the retry stream. It is called
// from the single final release seam (emitTrafficPTC), so live, gated,
// tool-finalizer, and recovery drains are observed exactly once and the
// returned event is never altered (requirements 3.3, 8.4). The detector is
// committed even when no metadata observers are configured; dispatch is only
// the optional public side effect.
func (s *retryRecvStream) observeCompactionRelease(ctx context.Context, ev lipapi.Event) {
	if s == nil || s.executor == nil || s.executor.CompactionRuntime.Detector == nil {
		return
	}
	observers := s.executor.compactionObservers()
	meta := compactiondetect.ResponseMeta{
		TraceID:    s.traceID,
		ALegID:     s.aLegID,
		BLegID:     s.bleg.BLegID,
		AttemptSeq: s.bleg.Seq,
		SessionID:  s.baseline.Session.AuthoritativeSessionID,
	}
	events := s.executor.CompactionRuntime.Detector.ResponseReleased(meta, ev)
	if len(events) == 0 {
		return
	}
	compaction.Dispatch(ctx, observers, events)
}
