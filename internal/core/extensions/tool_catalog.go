package extensions

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// RunToolCatalogFilterStage runs tool catalog filters in stable order (design §4, §17).
// After the chain, [lipapi.ReconcileToolChoiceAfterToolListChange] runs once, then the call is validated.
func RunToolCatalogFilterStage(ctx context.Context, log *slog.Logger, obs StageMetrics, filters []toolcatalog.Filter, call *lipapi.Call, meta toolcatalog.CatalogMeta, svc toolcatalog.Services) (err error) {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "lip.extension.tool_catalog")
	defer func() {
		if obs != nil {
			obs.ObserveStage(StageToolCatalog, outcome, time.Since(start).Seconds())
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	sorted := toolcatalog.MaterializeSorted(filters)
	for _, f := range sorted {
		if f == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, f.ID()) {
			continue
		}
		hErr := f.Handle(ctx, call, meta, svc)
		if hErr != nil {
			mode := f.FailureMode()
			if mode == sdkhooks.FailureModeUnspecified {
				mode = sdkhooks.FailOpen
			}
			if mode == sdkhooks.FailOpen {
				if log != nil {
					log.WarnContext(ctx, "tool_catalog_filter: handler error (fail-open)", "filter", f.ID(), "error", hErr)
				}
				if obs != nil {
					obs.IncFailOpenSkip(StageToolCatalog)
				}
				continue
			}
			outcome = "error"
			return fmt.Errorf("tool catalog filter %q: %w", f.ID(), hErr)
		}
	}
	lipapi.ReconcileToolChoiceAfterToolListChange(call)
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		return fmt.Errorf("extensions: invalid canonical call after tool catalog filters: %w", vErr)
	}
	return nil
}
