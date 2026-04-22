package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

var _ lipsdk.ExecutorView = (*Executor)(nil)

// Backend opens a canonical event stream for one route candidate.
type Backend struct {
	Caps lipapi.BackendCaps
	// ResolveCaps, when set, supplies model/candidate-aware capabilities; otherwise Caps is used.
	ResolveCaps func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps
	Open        func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.EventStream, error)
}

// BackendEffectiveCaps returns the caps used for negotiation for one backend and candidate.
func BackendEffectiveCaps(ctx context.Context, be Backend, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
	if be.ResolveCaps != nil {
		return be.ResolveCaps(ctx, call, cand)
	}
	return be.Caps
}

// Executor orchestrates hooks, capability negotiation, routing, B2BUA, and backend attempts.
type Executor struct {
	Store    b2bua.Store
	Bus      *hooks.Bus
	Backends map[string]Backend // key: routing.Primary.Backend (non-empty)
	// Rand supplies weighted routing rolls. Typical *rand/v2.Rand-backed values are not safe for
	// concurrent use; rng() wraps a non-nil Rand accordingly.
	Rand routing.Rng
	Now  func() time.Time
	// Log, when non-nil, receives structured orchestration decisions (diag.LogDecision).
	Log *slog.Logger

	// MaxAttempts caps B-leg opens per logical request (open + recv replacement). Zero means 3.
	MaxAttempts int
	// DefaultBackend resolves model-only selectors using routing.ApplyModelOnlyBackends (from config default_route).
	DefaultBackend string
	// CapsResolver, when set, supplies candidate-aware caps for negotiation; otherwise each
	// Backend's ResolveCaps / Caps is used via BackendEffectiveCaps.
	CapsResolver capabilities.Resolver
	// CandidateHealth, when set, supplies unhealthy routing keys merged into planner options.
	CandidateHealth policy.CandidateHealth
	// RouteObserver, when set, receives coarse routing decisions (non-blocking contract).
	RouteObserver lipsdk.RouteObserver
	// RouteTrace, when set, records recent routing decisions for diagnostics HTTP handlers.
	RouteTrace *diag.RouteTraceBuffer

	rngOnce    sync.Once
	lockedRand routing.Rng // lazy: mutex-serialized view of Rand
}

func (e *Executor) capsForAttempt(ctx context.Context, be Backend, attempt lipapi.Call, c routing.AttemptCandidate) lipapi.BackendCaps {
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, c, attempt)
	}
	return BackendEffectiveCaps(ctx, be, attempt, c)
}

// Execute runs submit hooks, resolves the A-leg, plans routes, negotiates per attempt,
// and returns a stream. Recoverable pre-output failures may consume additional B-legs
// before the returned stream yields events.
//
// ctx must be non-nil (same contract as [lipapi.EventStream.Recv]); nil returns [lipapi.ErrNilContext].
func (e *Executor) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	if e == nil || e.Store == nil || call == nil {
		return nil, fmt.Errorf("executor: invalid arguments")
	}
	if e.Bus == nil {
		return nil, fmt.Errorf("executor: nil hook bus")
	}
	bus := e.Bus
	if err := call.Validate(); err != nil {
		return nil, fmt.Errorf("executor: validate call: %w", err)
	}
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	traceID, baseline, aLeg, ctx, err := e.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		return nil, fmt.Errorf("executor: prepare submit: %w", err)
	}
	sel, err := routing.Parse(baseline.Route.Selector)
	if err != nil {
		return nil, fmt.Errorf("executor: parse route selector: %w", err)
	}
	routing.ApplyModelOnlyBackends(sel, e.DefaultBackend)
	if routing.SelectorHasEmptyBackend(sel) {
		return nil, fmt.Errorf("executor: %w", lipapi.ErrUnresolvedModelOnlySelector)
	}
	budget := &attemptBudget{max: e.effectiveMaxAttempts(), used: 0}
	session := &routing.SessionRoutingState{FirstRequestConsumed: aLeg.WeightedFirstConsumed}
	excluded := map[string]struct{}{}
	var lastReject lipapi.NegotiationResult
	rng := e.rng()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := e.tryPlanOpenOnce(attemptOpenParams{
			ctx:         ctx,
			bus:         bus,
			traceID:     traceID,
			aLegID:      aLeg.ALegID,
			baseline:    baseline,
			sel:         sel,
			session:     session,
			excluded:    excluded,
			rng:         rng,
			budget:      budget,
			isRetryPath: false,
			lastReject:  &lastReject,
		})
		if err != nil {
			return nil, fmt.Errorf("executor: plan or open attempt: %w", err)
		}
		if !out.opened {
			continue
		}
		rs := &retryRecvStream{
			executor: e,
			bus:      bus,
			baseline: baseline,
			budget:   budget,
			aLegID:   aLeg.ALegID,
			traceID:  traceID,
			sel:      sel,
			session:  session,
			excluded: excluded,
			rng:      rng,
			bleg:     out.bleg,
			cand:     out.cand,
		}
		rs.storeInner(out.stream)
		return rs, nil
	}
}
