package lipsdk

import "context"

// RouteObserver receives lightweight, structured routing/orchestration notifications.
// Implementations must be non-blocking or very fast; the executor invokes them on the hot path only when non-nil.
type RouteObserver interface {
	ObserveRouteDecision(ctx context.Context, traceID, decision, detail string)
}
