package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func (e *Executor) candidateAttemptMeta(ctx context.Context, rf requestFacts, attempt lipapi.Call, c routing.AttemptCandidate, be execbackend.Backend) request.AttemptMeta {
	meta := request.AttemptMeta{
		TraceID:         rf.traceID,
		ALegID:          rf.aLegID,
		CandidateKey:    c.Key,
		BackendID:       strings.TrimSpace(c.Primary.Backend),
		BackendPrefixes: execbackend.CloneBackendPrefixes(be),
		Model:           strings.TrimSpace(c.Primary.Model),
		ReplaySupport:   execbackend.EffectiveReplaySupport(ctx, be, attempt, c),
		Scope:           rf.recvViews.Scope,
		Session: session.SessionView{
			AuthoritativeSessionID: strings.TrimSpace(attempt.Session.AuthoritativeSessionID),
			ClientSessionHint:      strings.TrimSpace(attempt.Session.ClientSessionID),
			ALegID:                 rf.aLegID,
		},
		Workspace: cloneWorkspaceView(rf.recvViews.Workspace),
	}
	if rf.recvViews.Session.AuthoritativeSessionID != "" || rf.recvViews.Session.ClientSessionHint != "" {
		meta.Session = cloneSessionView(rf.recvViews.Session)
		meta.Session.ALegID = rf.aLegID
	}
	return meta
}

func cloneSessionView(src session.SessionView) session.SessionView {
	src.Labels = maps.Clone(src.Labels)
	return src
}

func cloneWorkspaceView(src lipworkspace.WorkspaceView) lipworkspace.WorkspaceView {
	src.Markers = slices.Clone(src.Markers)
	src.Labels = maps.Clone(src.Labels)
	return src
}

func (e *Executor) noteAttemptTransformExclude(ctx context.Context, traceID string, c routing.AttemptCandidate, res extensions.AttemptTransformStageResult, failures *candidateFailureHistory) {
	diag.LogDecision(ctx, e.Log, "attempt_transform_exclude", diag.AttrOpts{CallID: traceID},
		slog.String("decision", "exclude_candidate"), slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend), slog.String("reason_code", res.ReasonCode),
		slog.String("provider_id", res.ProviderID))
	e.notePlanCandidate(ctx, traceID, c.Key, nil)
	if failures != nil && failures.TransformExcludes != nil {
		failures.TransformExcludes.noteTransform(res.ReasonCode)
	}
}

func pinCandidateRouteIdentity(attempt *lipapi.Call, baseline lipapi.Call) {
	if attempt != nil {
		attempt.Route = baseline.Route
	}
}

type postHookRederiveResult struct {
	excluded    bool
	facts       modelcatalog.EffectiveFacts
	preflight   accountingpreflight.Decision
	preflightOK bool
}

func (e *Executor) rederiveAfterRequestHooks(
	ctx context.Context,
	rf requestFacts,
	route routeFacts,
	attempt *lipapi.Call,
	c routing.AttemptCandidate,
	be execbackend.Backend,
	stickyBackendID string,
	stickyBinding bool,
	failures *candidateFailureHistory,
	parallel bool,
) (postHookRederiveResult, error) {
	var out postHookRederiveResult
	if attempt == nil {
		return out, fmt.Errorf("executor: nil attempt after hooks")
	}
	pinCandidateRouteIdentity(attempt, rf.baseline)
	if vErr := attempt.Validate(); vErr != nil {
		return out, fmt.Errorf("executor: post-hook validate: %w", vErr)
	}
	admitOut, admitPanicErr := safety.CallValue(
		safety.BoundaryBackend,
		"backend_candidate_admission",
		func() (candidateAdmissionOutcome, error) {
			return e.evaluateCandidateAdmission(ctx, rf.traceID, *attempt, c, be, capabilities.NewFailoverRequirementSet(*attempt)), nil
		},
	)
	if admitPanicErr != nil {
		var pe *safety.PanicError
		if errors.As(admitPanicErr, &pe) {
			if e != nil && e.Log != nil {
				attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: rf.traceID}})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				e.Log.LogAttrs(ctx, slog.LevelError, "isolated_panic_candidate_admission", attrs...)
			}
			diag.LogDecision(
				ctx, e.Log, "candidate_admission_panic_exclude", diag.AttrOpts{CallID: rf.traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			out.excluded = true
			if failures != nil && failures.TransformExcludes != nil {
				failures.TransformExcludes.noteOther()
			}
			return out, nil
		}
		return out, admitPanicErr
	}
	out.facts = admitOut.facts
	if admitOut.admitRes.Kind == lipapi.NegotiationReject {
		if !parallel {
			e.noteCandidateAdmissionReject(ctx, rf.traceID, route.affinityKey, route.affinitySet, c, stickyBackendID, stickyBinding, admitOut, "post_request_hooks", failures)
		} else {
			reason := "admission_reject"
			if admitOut.admitRes.Transport.Kind == lipapi.NegotiationReject {
				reason = "transport_reject"
				if failures != nil {
					failures.TransportReject = admitOut.admitRes.Transport
				}
			} else if admitOut.admitRes.Capability.Kind == lipapi.NegotiationReject {
				reason = "capability_reject"
				if failures != nil {
					failures.CapabilityReject = admitOut.admitRes.Capability
				}
			} else if admitOut.admitRes.Requirements.Kind == lipapi.NegotiationReject {
				reason = "requirements_reject"
				if failures != nil {
					failures.AdmissionErr = admitOut.admitRes.Requirements.Err()
				}
			} else if admitOut.admitRes.ProjectionError != nil {
				reason = "projection_reject"
				if failures != nil {
					failures.AdmissionErr = admitOut.admitRes.ProjectionError
				}
			}
			if stickyBinding && c.Primary.Backend == stickyBackendID && failures != nil {
				failures.AffinityReset = reason
			}
			if failures != nil && failures.TransformExcludes != nil {
				failures.TransformExcludes.noteOther()
			}
		}
		out.excluded = true
		return out, nil
	}
	if admitOut.admitRes.Capability.Kind == lipapi.NegotiationDowngrade {
		lipapi.ApplyNegotiatedDowngrades(attempt, admitOut.admitRes.Capability)
	}
	attempt.Invocation.TransportMode = admitOut.admitRes.Transport.Selected
	facts := admitOut.facts
	if e != nil && e.EligibilityResolver != nil {
		facts = e.effectiveFactsForAttempt(ctx, be, *attempt, c)
		out.facts = facts
		d := e.EligibilityResolver.Check(ctx, c, *attempt, facts)
		if !d.IsEligible {
			if stickyBinding && c.Primary.Backend == stickyBackendID {
				if !parallel {
					e.clearAffinityBinding(ctx, rf.traceID, route.affinityKey, route.affinitySet, string(d.Reason))
				} else if failures != nil {
					failures.AffinityReset = string(d.Reason)
				}
			}
			if failures != nil && d.Reason == modelcatalog.EligibilityContextLimitExceeded {
				failures.ContextLimit = true
			}
			diag.LogDecision(ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: rf.traceID},
				slog.String("candidate_key", c.Key), slog.String("backend", c.Primary.Backend),
				slog.String("phase", "post_request_hooks"))
			if failures != nil && failures.TransformExcludes != nil {
				failures.TransformExcludes.noteOther()
			}
			out.excluded = true
			return out, nil
		}
	}
	if decision, ok := e.runPreflight(ctx, rf.traceID, *attempt, c, facts.Facts); ok {
		out.preflight, out.preflightOK = decision, true
		if !decision.Allowed {
			return out, fmt.Errorf("executor: token accounting preflight: %w", decision.Err)
		}
		if decision.AdjustedMaxOutputTokens != nil {
			adjusted := *decision.AdjustedMaxOutputTokens
			attempt.Options.MaxOutputTokens = &adjusted
		}
	}
	return out, nil
}
