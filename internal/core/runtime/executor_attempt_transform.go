package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func (e *Executor) candidateAttemptMeta(ctx context.Context, p attemptOpenParams, attempt lipapi.Call, c routing.AttemptCandidate, be execbackend.Backend) request.AttemptMeta {
	meta := request.AttemptMeta{
		TraceID:         p.traceID,
		ALegID:          p.aLegID,
		CandidateKey:    c.Key,
		BackendID:       strings.TrimSpace(c.Primary.Backend),
		BackendPrefixes: execbackend.CloneBackendPrefixes(be),
		Model:           strings.TrimSpace(c.Primary.Model),
		ReplaySupport:   execbackend.EffectiveReplaySupport(ctx, be, attempt, c),
		Scope:           scopeFromCtx(ctx),
		Session: session.SessionView{
			AuthoritativeSessionID: strings.TrimSpace(attempt.Session.AuthoritativeSessionID),
			ClientSessionHint:      strings.TrimSpace(attempt.Session.ClientSessionID),
			ALegID:                 p.aLegID,
		},
		Workspace: lipworkspace.WorkspaceView{},
	}
	if v, ok := execctx.FromContext(ctx); ok {
		meta.Workspace = cloneWorkspaceView(v.Workspace)
		meta.Scope = v.Scope
		if v.Session.AuthoritativeSessionID != "" || v.Session.ClientSessionHint != "" {
			meta.Session = cloneSessionView(v.Session)
			meta.Session.ALegID = p.aLegID
		}
	}
	return meta
}

func cloneSessionView(in session.SessionView) session.SessionView {
	out := in
	out.Labels = maps.Clone(in.Labels)
	return out
}

func cloneWorkspaceView(in lipworkspace.WorkspaceView) lipworkspace.WorkspaceView {
	out := in
	out.Markers = slices.Clone(in.Markers)
	out.Labels = maps.Clone(in.Labels)
	return out
}

func (e *Executor) noteAttemptTransformExclude(ctx context.Context, p attemptOpenParams, c routing.AttemptCandidate, res extensions.AttemptTransformStageResult) {
	diag.LogDecision(ctx, e.Log, "attempt_transform_exclude", diag.AttrOpts{CallID: p.traceID},
		slog.String("decision", "exclude_candidate"), slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend), slog.String("reason_code", res.ReasonCode),
		slog.String("provider_id", res.ProviderID))
	e.notePlanCandidate(ctx, p.traceID, c.Key, nil)
	if p.transformExcludes != nil {
		p.transformExcludes.noteTransform(res.ReasonCode)
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
	p attemptOpenParams,
	attempt *lipapi.Call,
	c routing.AttemptCandidate,
	be execbackend.Backend,
	stickyBackendID string,
	stickyBinding bool,
) (postHookRederiveResult, error) {
	var out postHookRederiveResult
	if attempt == nil {
		return out, fmt.Errorf("executor: nil attempt after hooks")
	}
	pinCandidateRouteIdentity(attempt, p.baseline)
	if vErr := attempt.Validate(); vErr != nil {
		return out, fmt.Errorf("executor: post-hook validate: %w", vErr)
	}
	admitOut := e.evaluateCandidateAdmission(ctx, p.traceID, *attempt, c, be, p.failoverReq)
	out.facts = admitOut.facts
	if admitOut.admitRes.Kind == lipapi.NegotiationReject {
		e.noteCandidateAdmissionReject(ctx, p, c, stickyBackendID, stickyBinding, admitOut, "post_request_hooks")
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
				e.clearAffinityBinding(ctx, p.traceID, p.affinityKey, p.affinitySet, string(d.Reason))
			}
			if p.isContextLimitExhaustion != nil && d.Reason == modelcatalog.EligibilityContextLimitExceeded {
				*p.isContextLimitExhaustion = true
			}
			diag.LogDecision(ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: p.traceID},
				slog.String("candidate_key", c.Key), slog.String("backend", c.Primary.Backend),
				slog.String("phase", "post_request_hooks"))
			if p.transformExcludes != nil {
				p.transformExcludes.noteOther()
			}
			out.excluded = true
			return out, nil
		}
	}
	if decision, ok := e.runPreflight(ctx, p.traceID, *attempt, c, facts.Facts); ok {
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
