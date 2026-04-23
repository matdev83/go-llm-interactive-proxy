package extensions

import (
	"context"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const otelScopeExtensions = "github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"

// RunSessionOpenStage invokes session openers in registration order with fail-open semantics
// (design §17 session_open default).
func RunSessionOpenStage(ctx context.Context, log *slog.Logger, obs StageMetrics, openers []session.Opener, in session.OpenInput) session.OpenResult {
	start := time.Now()
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "lip.extension.session_open")
	defer func() {
		if obs != nil {
			obs.ObserveStage(StageSessionOpen, "ok", time.Since(start).Seconds())
		}
		span.SetStatus(codes.Ok, "")
		span.End()
	}()

	var acc session.OpenResult
	for _, o := range openers {
		if o == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, o.ID()) {
			continue
		}
		r, err := o.Open(ctx, in)
		if err != nil {
			if log != nil {
				log.WarnContext(ctx, "session_open: opener error (fail-open)", "opener", o.ID(), "error", err)
			}
			if obs != nil {
				obs.IncFailOpenSkip(StageSessionOpen)
			}
			continue
		}
		acc = acc.Merge(r)
	}
	return acc
}
