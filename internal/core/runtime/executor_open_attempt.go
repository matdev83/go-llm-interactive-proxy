package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	coretraffic "github.com/matdev83/go-llm-interactive-proxy/internal/core/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type attemptOpenParams struct {
	ctx         context.Context
	bus         *hooks.Bus
	traceID     string
	aLegID      string
	baseline    lipapi.Call
	sel         *routing.Selector
	requestSize routing.RequestSizeEstimate
	session     *routing.SessionRoutingState
	excluded    map[string]struct{}
	rng         routing.Rng
	budget      *attemptBudget
	isRetryPath bool
	lastReject  *lipapi.NegotiationResult
	// isContextLimitExhaustion, when non-nil, is set true when excluding a candidate for context-limit
	// eligibility so a subsequent ErrNoEligibleCandidate maps to [lipapi.ErrAllCandidatesContextLimitExceeded].
	isContextLimitExhaustion *bool
}

type attemptOpenResult struct {
	opened bool
	stream lipapi.EventStream
	bleg   b2bua.BLegRecord
	cand   routing.AttemptCandidate
}

func (e *Executor) tryPlanOpenOnce(p attemptOpenParams) (attemptOpenResult, error) {
	var zero attemptOpenResult
	list, err := routing.ExpandFailover(p.sel, routing.PlanOptions{
		Excluded:               p.excluded,
		Unhealthy:              e.mergePlannerHealth(),
		RequestSize:            p.requestSize,
		Session:                p.session,
		PreferredCandidateKeys: execctx.RouteCandidatePreferences(p.ctx),
		Rand:                   p.rng,
		IsRetryPath:            p.isRetryPath,
	})
	if err != nil {
		noEligible := errors.Is(err, routing.ErrNoEligibleCandidate)
		lastNegotiationReject := p.lastReject != nil && p.lastReject.Kind == lipapi.NegotiationReject
		if noEligible && lastNegotiationReject {
			return zero, p.lastReject.Err()
		}
		if noEligible && p.isContextLimitExhaustion != nil && *p.isContextLimitExhaustion {
			return zero, lipapi.ErrAllCandidatesContextLimitExceeded
		}
		return zero, fmt.Errorf("executor: expand failover: %w", err)
	}
	c := list[0]
	if p.isContextLimitExhaustion != nil {
		*p.isContextLimitExhaustion = false
	}
	attempt := lipapi.CloneCall(p.baseline)
	if e != nil && e.MaxPendingWireEvents > 0 {
		attempt.MaxPendingWireEvents = e.MaxPendingWireEvents
	}
	req := lipapi.RequiredCapabilities(attempt)
	be, ok := e.Backends[c.Primary.Backend]
	if !ok {
		return zero, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
	}
	var facts modelcatalog.EffectiveFacts
	res, negotiatePanicErr := safety.CallValue(
		safety.BoundaryBackend,
		"backend_capability_negotiate",
		func() (lipapi.NegotiationResult, error) {
			facts = e.effectiveFactsForAttempt(p.ctx, be, attempt, c)
			return lipapi.Negotiate(req, facts.EffectiveCaps), nil
		},
	)
	if negotiatePanicErr != nil {
		var pe *safety.PanicError
		if errors.As(negotiatePanicErr, &pe) {
			if e != nil && e.Log != nil {
				attrs := diag.IsolatedCrashAttrs(p.ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: p.traceID}})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				e.Log.LogAttrs(p.ctx, slog.LevelError, "isolated_panic_capability_negotiate", attrs...)
			}
			diag.LogDecision(p.ctx, e.Log, "capability_negotiate_panic_exclude", diag.AttrOpts{CallID: p.traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			p.excluded[c.Key] = struct{}{}
			return zero, nil
		}
		return zero, negotiatePanicErr
	}
	if res.Kind == lipapi.NegotiationReject {
		if p.lastReject != nil {
			*p.lastReject = res
		}
		diag.LogDecision(p.ctx, e.Log, "capability_reject", diag.AttrOpts{CallID: p.traceID},
			slog.String("decision", "exclude_candidate"),
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		// Req 9.3 / task 6.2: same route-trace surface as context exclusions (negotiation outcome + catalog metadata).
		cat := catalogRouteTraceIfEnabled(e, facts, res, nil, false)
		e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
		p.excluded[c.Key] = struct{}{}
		return zero, nil
	}
	if p.lastReject != nil {
		*p.lastReject = lipapi.NegotiationResult{}
	}
	if res.Kind == lipapi.NegotiationDowngrade {
		diag.LogDecision(p.ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: p.traceID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
		lipapi.ApplyNegotiatedDowngrades(&attempt, res)
	}
	var elig *modelcatalog.EligibilityDecision
	eligRan := e != nil && e.EligibilityResolver != nil
	if eligRan {
		facts = e.effectiveFactsForAttempt(p.ctx, be, attempt, c)
		d := e.EligibilityResolver.Check(p.ctx, c, attempt, facts)
		elig = &d
		if !d.IsEligible {
			if p.isContextLimitExhaustion != nil && d.Reason == modelcatalog.EligibilityContextLimitExceeded {
				*p.isContextLimitExhaustion = true
			}
			diag.LogDecision(p.ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: p.traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			cat := catalogRouteTraceIfEnabled(e, facts, res, elig, true)
			e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
			p.excluded[c.Key] = struct{}{}
			return zero, nil
		}
	}
	if res.Kind == lipapi.NegotiationDowngrade && !eligRan && e != nil {
		facts = e.effectiveFactsForAttempt(p.ctx, be, attempt, c)
	}
	cat := catalogRouteTraceIfEnabled(e, facts, res, elig, eligRan)
	e.notePlanCandidate(p.ctx, p.traceID, c.Key, cat)
	if c.MarkedFirst {
		if err := e.Store.SetWeightedFirstConsumed(p.ctx, p.aLegID, true); err != nil {
			return zero, fmt.Errorf("executor: set weighted first consumed: %w", err)
		}
		p.session.FirstRequestConsumed = true
	}
	if !p.budget.tryAcquire() {
		return zero, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
	}
	bleg, err := e.Store.NextBLeg(p.ctx, p.aLegID)
	if err != nil {
		return zero, fmt.Errorf("executor: next b-leg: %w", err)
	}
	hookCtx := p.ctx
	if e != nil && e.Log != nil {
		hookCtx = hooks.WithDiagnosticsLogger(p.ctx, e.Log)
	}
	if err := p.bus.RunRequestPartHooks(hookCtx, &attempt, sdk.PartMeta{
		TraceID:    p.traceID,
		ALegID:     p.aLegID,
		BLegID:     bleg.BLegID,
		AttemptSeq: bleg.Seq,
	}); err != nil {
		return zero, fmt.Errorf("executor: request hooks: %w", err)
	}
	openCall, err := backendCallWithRouteParams(attempt, c)
	if err != nil {
		return zero, fmt.Errorf("executor: %w", err)
	}
	if e.RuntimeSnapshot != nil {
		if rawPayload, jerr := json.Marshal(openCall); jerr == nil {
			meta := sdktraffic.CaptureMeta{
				TraceID:    p.traceID,
				ALegID:     p.aLegID,
				BLegID:     bleg.BLegID,
				AttemptSeq: bleg.Seq,
				BackendID:  strings.TrimSpace(c.Primary.Backend),
			}
			coretraffic.PortBundleFromSnapshot(e.RuntimeSnapshot).Emit(
				p.ctx,
				sdktraffic.LegPTB,
				meta,
				"lip/canonical+json",
				"application/json",
				rawPayload,
			)
		}
	}
	openCtx, openSpan := otel.Tracer(otelScopeExecutor).Start(p.ctx, "lip.executor.backend_open",
		trace.WithAttributes(
			attribute.String("lip.backend", c.Primary.Backend),
			attribute.Int("lip.b_leg_seq", int(bleg.Seq)),
		),
	)
	defer openSpan.End()
	openStart := time.Now()
	stream, err := safety.CallValue(safety.BoundaryBackend, "backend_open", func() (lipapi.EventStream, error) {
		return be.Open(openCtx, openCall, c)
	})
	openDur := time.Since(openStart).Seconds()
	if err != nil {
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			err = mapBackendPanic(pe, false, c.Key)
		}
	}
	if e != nil && e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}
	if err != nil {
		openSpan.RecordError(err)
		openSpan.SetStatus(codes.Error, "backend open failed")
		if lipapi.IsRecoverablePreOutput(err) {
			e.recordAttemptLogged(p.ctx, recordAttemptParams{
				ALegID:    p.aLegID,
				BLeg:      bleg,
				Cand:      c,
				Outcome:   lipapi.AttemptSwallowedFailure,
				Reason:    "recoverable pre-output (open)",
				DetailErr: err,
			}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
			diag.LogDecision(p.ctx, e.Log, "recoverable_pre_output_swallowed",
				diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			p.excluded[c.Key] = struct{}{}
			return zero, nil
		}
		e.recordAttemptLogged(p.ctx, recordAttemptParams{
			ALegID:    p.aLegID,
			BLeg:      bleg,
			Cand:      c,
			Outcome:   lipapi.AttemptSurfacedFailure,
			Reason:    attemptReasonDetail(err),
			DetailErr: err,
		}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
		return zero, fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, err)
	}
	if m := e.secureSessionForAttempt(); m != nil {
		if st, ok := execctx.SecureSessionTurnFromContext(openCtx); ok {
			tr := buildAttemptTrace(st, p.aLegID, bleg, c, openCall, openStart)
			persistCtx := context.WithoutCancel(openCtx)
			if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
				e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
			}
		}
	}
	diag.LogDecision(p.ctx, e.Log, "backend_stream_opened", diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("model", c.Primary.Model),
	)
	return attemptOpenResult{opened: true, stream: stream, bleg: bleg, cand: c}, nil
}

func (e *Executor) requestSizeEstimateForRouting(ctx context.Context, sel *routing.Selector, call lipapi.Call) routing.RequestSizeEstimate {
	if !routing.SelectorHasRequestSizeConstraints(sel) || e.RequestTokenEstimator == nil {
		return routing.RequestSizeEstimate{}
	}
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, call)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}
