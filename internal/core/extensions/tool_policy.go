package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/extensiontrace"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"go.opentelemetry.io/otel/attribute"
)

// ToolPolicyStageInput carries inputs for [RunToolPolicyStage].
type ToolPolicyStageInput struct {
	Ctx      context.Context
	Log      *slog.Logger
	Obs      StageMetrics
	Policies []toolpolicy.Policy // execution order; see RunToolPolicyStage
	Event    lipapi.ToolEvent
	Meta     toolpolicy.Meta
	Svc      toolpolicy.Services
}

// RunToolPolicyStage runs provider-neutral tool-call policies before tool reactors.
// in.Policies must already be in execution order (as produced by [toolpolicy.MaterializeSorted] or
// by [RequestRuntimeSnapshot.ToolCallPoliciesExecution]); the stage does not re-sort.
func RunToolPolicyStage(in ToolPolicyStageInput) (err error) {
	ctx := in.Ctx
	log := in.Log
	obs := in.Obs
	policies := in.Policies
	event := in.Event
	meta := in.Meta
	svc := in.Svc
	if ctx == nil {
		return fmt.Errorf("extensions: %w", lipapi.ErrNilContext)
	}
	if err := hooks.ValidateToolEventAfterPolicy("tool_policy:input", &event); err != nil {
		return err
	}
	start := time.Now()
	outcome := "ok"
	ctx, endSpan := extensiontrace.StartSpan(ctx, "lip.extension.tool_policy",
		attribute.String("lip.extension.stage", "tool_policy"),
		attribute.Int("lip.extension.tool_policy.policy_count", len(policies)),
	)
	defer func() {
		if obs != nil {
			obs.ObserveStage(StageToolEventReaction, outcome, time.Since(start).Seconds())
		}
		endSpan(err)
	}()

	for _, p := range policies {
		if p == nil {
			continue
		}
		if execctx.IsSuppressedPluginID(ctx, p.ID()) {
			continue
		}
		decision, hErr := safety.CallValue(safety.BoundaryExtension, "tool_policy", func() (toolpolicy.Decision, error) {
			return p.Handle(ctx, event, meta, svc)
		})
		if hErr != nil {
			mode := p.FailureMode()
			if mode == sdkhooks.FailureModeUnspecified {
				mode = sdkhooks.FailClosed
			}
			if mode == sdkhooks.FailOpen {
				if log != nil {
					var pe *safety.PanicError
					if errors.As(hErr, &pe) {
						logFailOpenExtensionPanic(ctx, log, "tool_policy", p.ID(), hErr)
					} else {
						log.WarnContext(ctx, "tool_policy: handler error (fail-open)", "policy", p.ID(), "error", hErr)
					}
				}
				if obs != nil {
					obs.IncFailOpenSkip(StageToolEventReaction)
				}
				continue
			}
			outcome = "error"
			return fmt.Errorf("tool policy %q: %w", p.ID(), hErr)
		}
		switch decision {
		case toolpolicy.DecisionAllow, toolpolicy.DecisionUnspecified:
			continue
		case toolpolicy.DecisionDeny:
			outcome = "denied"
			return fmt.Errorf("tool policy %q denied tool call %q", p.ID(), event.ToolName)
		default:
			outcome = "error"
			return fmt.Errorf("tool policy %q returned unknown decision %d", p.ID(), decision)
		}
	}
	return nil
}
