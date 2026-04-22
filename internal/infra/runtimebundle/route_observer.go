package runtimebundle

import (
	"context"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

type noopRouteObserver struct{}

var _ lipsdk.RouteObserver = noopRouteObserver{}

func (noopRouteObserver) ObserveRouteDecision(context.Context, string, string, string) {}

type slogRouteObserver struct {
	log *slog.Logger
}

var _ lipsdk.RouteObserver = (*slogRouteObserver)(nil)

func (o slogRouteObserver) ObserveRouteDecision(ctx context.Context, traceID, decision, detail string) {
	o.log.LogAttrs(ctx, slog.LevelInfo, "lip.route",
		slog.String("trace_id", traceID),
		slog.String("decision", decision),
		slog.String("detail", detail),
	)
}

func routeObserverFor(log *slog.Logger) lipsdk.RouteObserver {
	if log == nil {
		return noopRouteObserver{}
	}
	return slogRouteObserver{log: log}
}
