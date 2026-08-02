package runtime

import (
	"context"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type candidateAdmissionOutcome struct {
	admitRes      lipapi.CandidateAdmissionResult
	facts         modelcatalog.EffectiveFacts
	transportMode lipapi.TransportMode
}

func (e *Executor) evaluateCandidateAdmission(
	ctx context.Context,
	traceID string,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
	be execbackend.Backend,
	failoverReq capabilities.FailoverRequirementSet,
) candidateAdmissionOutcome {
	facts := e.effectiveFactsForAttempt(ctx, be, attempt, c)
	replay := execbackend.EffectiveReplaySupport(ctx, be, attempt, c)
	transportCaps := e.transportCapsForAttempt(ctx, be, attempt, c)
	target := lipapi.LegacyProjectionTargetFromCaps(facts.EffectiveCaps, replay)
	target.SupportedExtensions = append([]lipapi.ExtensionRequirement(nil), execbackend.EffectiveDialectSupport(ctx, be, attempt, c).ExtensionTypes...)
	frozen := failoverReq.Required
	admitRes := capabilities.AdmitCandidate(ctx, attempt, attempt.Invocation, c, capabilities.CandidateFacts{
		Caps:               facts.EffectiveCaps,
		TransportCaps:      transportCaps,
		ReplaySupport:      replay,
		DialectSupport:     execbackend.EffectiveDialectSupport(ctx, be, attempt, c),
		ProjectionTarget:   target,
		FrozenRequirements: &frozen,
	})
	mode := admitRes.Transport.Selected
	if mode == "" {
		mode = admitRes.Transport.Mode
	}
	return candidateAdmissionOutcome{
		admitRes:      admitRes,
		facts:         facts,
		transportMode: mode,
	}
}

func (e *Executor) noteCandidateAdmissionReject(
	ctx context.Context,
	p attemptOpenParams,
	c routing.AttemptCandidate,
	stickyBackendID string,
	stickyBinding bool,
	out candidateAdmissionOutcome,
	phase string,
) {
	reason := "admission_reject"
	if out.admitRes.Transport.Kind == lipapi.NegotiationReject {
		reason = "transport_reject"
		e.recordTransportNegotiation(out.admitRes.Transport.Operation, out.admitRes.Transport.Mode, "reject")
		if p.lastTransportReject != nil {
			*p.lastTransportReject = out.admitRes.Transport
		}
	} else if out.admitRes.Capability.Kind == lipapi.NegotiationReject {
		reason = "capability_reject"
		if p.lastReject != nil {
			*p.lastReject = out.admitRes.Capability
		}
	} else if out.admitRes.Requirements.Kind == lipapi.NegotiationReject {
		reason = "requirements_reject"
		if p.lastAdmissionErr != nil {
			*p.lastAdmissionErr = out.admitRes.Requirements.Err()
		}
	} else if out.admitRes.ProjectionError != nil {
		reason = "projection_reject"
		if p.lastAdmissionErr != nil {
			*p.lastAdmissionErr = out.admitRes.ProjectionError
		}
	}
	if stickyBinding && c.Primary.Backend == stickyBackendID {
		e.clearAffinityBinding(ctx, p.traceID, p.affinityKey, p.affinitySet, reason)
	}
	diag.LogDecision(
		ctx, e.Log, reason, diag.AttrOpts{CallID: p.traceID},
		slog.String("decision", "exclude_candidate"),
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("phase", phase),
	)
	cat := catalogRouteTraceIfEnabled(e, out.facts, out.admitRes.Capability, nil, false)
	e.notePlanCandidate(ctx, p.traceID, c.Key, cat)
	if p.transformExcludes != nil {
		p.transformExcludes.noteOther()
	}
}
