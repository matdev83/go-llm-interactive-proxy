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
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
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
	session     *routing.SessionRoutingState
	excluded    map[string]struct{}
	rng         routing.Rng
	budget      *attemptBudget
	isRetryPath bool
	lastReject  *lipapi.NegotiationResult
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
		Session:                p.session,
		PreferredCandidateKeys: execctx.RouteCandidatePreferences(p.ctx),
		Rand:                   p.rng,
		IsRetryPath:            p.isRetryPath,
	})
	if err != nil {
		if errors.Is(err, routing.ErrNoEligibleCandidate) && p.lastReject != nil && p.lastReject.Kind == lipapi.NegotiationReject {
			return zero, p.lastReject.Err()
		}
		return zero, fmt.Errorf("executor: expand failover: %w", err)
	}
	c := list[0]
	e.notePlanCandidate(p.ctx, p.traceID, c.Key)
	attempt := lipapi.CloneCall(p.baseline)
	if e != nil && e.MaxPendingWireEvents > 0 {
		attempt.MaxPendingWireEvents = e.MaxPendingWireEvents
	}
	req := lipapi.RequiredCapabilities(attempt)
	be, ok := e.Backends[c.Primary.Backend]
	if !ok {
		return zero, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
	}
	res := lipapi.Negotiate(req, e.capsForAttempt(p.ctx, be, attempt, c))
	if res.Kind == lipapi.NegotiationReject {
		if p.lastReject != nil {
			*p.lastReject = res
		}
		diag.LogDecision(p.ctx, e.Log, "capability_reject", diag.AttrOpts{CallID: p.traceID},
			slog.String("decision", "exclude_candidate"),
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
		)
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
	if err := p.bus.RunRequestPartHooks(p.ctx, &attempt, sdk.PartMeta{
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
	stream, err := be.Open(openCtx, openCall, c)
	openDur := time.Since(openStart).Seconds()
	if e != nil && e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}
	if err != nil {
		openSpan.RecordError(err)
		openSpan.SetStatus(codes.Error, err.Error())
		if lipapi.IsRecoverablePreOutput(err) {
			e.recordAttemptLogged(p.ctx, recordAttemptParams{
				ALegID:  p.aLegID,
				BLeg:    bleg,
				Cand:    c,
				Outcome: lipapi.AttemptSwallowedFailure,
				Reason:  "recoverable pre-output (open)",
			}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
			diag.LogDecision(p.ctx, e.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			p.excluded[c.Key] = struct{}{}
			return zero, nil
		}
		e.recordAttemptLogged(p.ctx, recordAttemptParams{
			ALegID:  p.aLegID,
			BLeg:    bleg,
			Cand:    c,
			Outcome: lipapi.AttemptSurfacedFailure,
			Reason:  attemptReasonDetail(err),
		}, diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID})
		return zero, fmt.Errorf("executor: backend open %q: %w", c.Primary.Backend, err)
	}
	diag.LogDecision(p.ctx, e.Log, "backend_stream_opened", diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("model", c.Primary.Model),
	)
	return attemptOpenResult{opened: true, stream: stream, bleg: bleg, cand: c}, nil
}
