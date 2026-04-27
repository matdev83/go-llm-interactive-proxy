package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func (e *Executor) observeRoute(ctx context.Context, traceID, decision, detail string) {
	if e == nil || e.RouteObserver == nil {
		return
	}
	e.RouteObserver.ObserveRouteDecision(ctx, traceID, decision, detail)
}

func (e *Executor) notePlanCandidate(ctx context.Context, traceID, candidateKey string, cat *diag.RouteTraceCatalog) {
	if e == nil {
		return
	}
	if e.RouteTrace != nil {
		e.RouteTrace.Append(diag.RouteTraceEntry{
			TraceID:  traceID,
			Decision: "plan_candidate",
			Detail:   candidateKey,
			Catalog:  cat,
		})
	}
	if e.RouteObserver != nil {
		e.RouteObserver.ObserveRouteDecision(ctx, traceID, "plan_candidate", candidateKey)
	}
}
