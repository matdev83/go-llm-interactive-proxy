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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

// RunPreRequestStage runs admission handlers in stable order after request shaping and before route planning.
func RunPreRequestStage(ctx context.Context, log *slog.Logger, obs StageMetrics, handlers []prerequest.Handler, call *lipapi.Call, meta prerequest.Meta, svc prerequest.Services) (err error) {
	if call == nil {
		return fmt.Errorf("extensions: nil call: %w", lipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	if execctx.AuxiliaryDepth(ctx) > 0 {
		return nil
	}

	start := time.Now()
	outcome := "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "lip.extension.pre_request")
	defer func() {
		if obs != nil {
			obs.ObserveStage(MetricsStagePreRequest, outcome, time.Since(start).Seconds())
		}
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	for _, h := range prerequest.MaterializeSorted(handlers) {
		if h == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, h.ID()) {
			continue
		}
		decision, hErr := safety.CallValue(safety.BoundaryExtension, "pre_request", func() (prerequest.Decision, error) {
			return h.Handle(ctx, call, meta, svc)
		})
		if hErr != nil {
			mode := h.FailureMode()
			if mode == sdkhooks.FailureModeUnspecified {
				mode = sdkhooks.FailOpen
			}
			if mode == sdkhooks.FailOpen {
				if log != nil {
					var pe *safety.PanicError
					if errors.As(hErr, &pe) {
						logFailOpenExtensionPanic(ctx, log, "pre_request", h.ID(), hErr)
					} else {
						log.WarnContext(ctx, "pre_request: handler error (fail-open)", "handler", h.ID(), "error", hErr)
					}
				}
				if obs != nil {
					obs.IncFailOpenSkip(MetricsStagePreRequest)
				}
				continue
			}
			outcome = "error"
			return fmt.Errorf("pre-request handler %q: %w", h.ID(), hErr)
		}
		if len(decision.Annotations) != 0 {
			if meta.Annotations == nil {
				meta.Annotations = make(map[string]string, len(decision.Annotations))
			}
			for k, v := range decision.Annotations {
				meta.Annotations[k] = v
			}
		}
		if decision.Deny {
			outcome = "denied"
			return prerequest.NewRejectError(h.ID(), decision.DenyMessage)
		}
	}
	if vErr := call.Validate(); vErr != nil {
		outcome = "error"
		return fmt.Errorf("extensions: invalid canonical call after pre-request: %w", vErr)
	}
	return nil
}
