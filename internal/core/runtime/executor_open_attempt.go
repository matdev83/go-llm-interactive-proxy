package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
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
		Excluded:    p.excluded,
		Unhealthy:   e.mergePlannerHealth(),
		Session:     p.session,
		Rand:        p.rng,
		IsRetryPath: p.isRetryPath,
	})
	if err != nil {
		if errors.Is(err, routing.ErrNoEligibleCandidate) && p.lastReject != nil && p.lastReject.Kind == lipapi.NegotiationReject {
			return zero, p.lastReject.Err()
		}
		return zero, err
	}
	c := list[0]
	e.notePlanCandidate(p.ctx, p.traceID, c.Key)
	attempt := lipapi.CloneCall(p.baseline)
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
			return zero, err
		}
		p.session.FirstRequestConsumed = true
	}
	if !p.budget.tryAcquire() {
		return zero, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
	}
	bleg, err := e.Store.NextBLeg(p.ctx, p.aLegID)
	if err != nil {
		return zero, err
	}
	if err := p.bus.RunRequestPartHooks(p.ctx, &attempt, sdk.PartMeta{
		TraceID:    p.traceID,
		ALegID:     p.aLegID,
		BLegID:     bleg.BLegID,
		AttemptSeq: bleg.Seq,
	}); err != nil {
		return zero, err
	}
	openCall, err := backendCallWithRouteParams(attempt, c)
	if err != nil {
		return zero, fmt.Errorf("executor: %w", err)
	}
	stream, err := be.Open(p.ctx, openCall, c)
	if err != nil {
		if lipapi.IsRecoverablePreOutput(err) {
			_ = e.recordAttempt(p.ctx, recordAttemptParams{
				ALegID:  p.aLegID,
				BLeg:    bleg,
				Cand:    c,
				Outcome: lipapi.AttemptSwallowedFailure,
				Reason:  "recoverable pre-output (open)",
			})
			diag.LogDecision(p.ctx, e.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
				slog.String("candidate_key", c.Key),
				slog.String("phase", "open"),
			)
			p.excluded[c.Key] = struct{}{}
			return zero, nil
		}
		_ = e.recordAttempt(p.ctx, recordAttemptParams{
			ALegID:  p.aLegID,
			BLeg:    bleg,
			Cand:    c,
			Outcome: lipapi.AttemptSurfacedFailure,
			Reason:  err.Error(),
		})
		return zero, err
	}
	diag.LogDecision(p.ctx, e.Log, "backend_stream_opened", diag.AttrOpts{CallID: p.traceID, BLegID: bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("model", c.Primary.Model),
	)
	return attemptOpenResult{opened: true, stream: stream, bleg: bleg, cand: c}, nil
}
