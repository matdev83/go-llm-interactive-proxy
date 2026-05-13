package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// RunRequestTransformStage runs request-wide transforms in stable order (design §5, §17).
// Errors from handlers with FailOpen are logged and skipped; FailClosed stops the chain.
// The call is re-validated after the chain completes.
func RunRequestTransformStage(ctx context.Context, log *slog.Logger, obs StageMetrics, transforms []request.Transform, call *lipapi.Call, meta request.RequestMeta, svc request.Services) (err error) {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "lip.extension.request_transform")
	defer func() {
		if obs != nil {
			obs.ObserveStage(MetricsStageRequestTransform, outcome, time.Since(start).Seconds())
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	sorted := request.MaterializeSorted(transforms)
	for _, tr := range sorted {
		if tr == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, tr.ID()) {
			continue
		}
		hErr := safety.Call(safety.BoundaryExtension, "request_transform", func() error {
			return tr.Handle(ctx, call, meta, svc)
		})
		if hErr != nil {
			mode := tr.FailureMode()
			if mode == sdkhooks.FailureModeUnspecified {
				mode = sdkhooks.FailOpen
			}
			if mode == sdkhooks.FailOpen {
				if log != nil {
					var pe *safety.PanicError
					if errors.As(hErr, &pe) {
						logFailOpenExtensionPanic(ctx, log, "request_transform", tr.ID(), hErr)
					} else {
						log.WarnContext(ctx, "request_transform: handler error (fail-open)", "transform", tr.ID(), "error", hErr)
					}
				}
				if obs != nil {
					obs.IncFailOpenSkip(MetricsStageRequestTransform)
				}
				continue
			}
			outcome = "error"
			return fmt.Errorf("request transform %q: %w", tr.ID(), hErr)
		}
	}
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		return fmt.Errorf("extensions: invalid canonical call after request transforms: %w", vErr)
	}
	return nil
}
