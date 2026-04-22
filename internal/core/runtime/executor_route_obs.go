package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func (e *Executor) traceRoute(traceID, decision, detail string) {
	if e == nil || e.RouteTrace == nil {
		return
	}
	e.RouteTrace.Append(diag.RouteTraceEntry{TraceID: traceID, Decision: decision, Detail: detail})
}

func (e *Executor) observeRoute(ctx context.Context, traceID, decision, detail string) {
	if e == nil || e.RouteObserver == nil {
		return
	}
	e.RouteObserver.ObserveRouteDecision(ctx, traceID, decision, detail)
}

func (e *Executor) notePlanCandidate(ctx context.Context, traceID, candidateKey string) {
	e.traceRoute(traceID, "plan_candidate", candidateKey)
	e.observeRoute(ctx, traceID, "plan_candidate", candidateKey)
}
