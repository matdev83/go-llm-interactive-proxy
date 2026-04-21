package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
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
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
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

func backendCaps(ctx context.Context, be Backend, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
	return BackendEffectiveCaps(ctx, be, call, cand)
}

// Executor orchestrates hooks, capability negotiation, routing, B2BUA, and backend attempts.
type Executor struct {
	Store    b2bua.Store
	Bus      *hooks.Bus
	Backends map[string]Backend // key: routing.Primary.Backend (non-empty)
	// Rand supplies weighted routing rolls. Common implementations (*math/rand.Rand)
	// are not safe for concurrent use; rng() wraps a non-nil Rand accordingly.
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

type attemptBudget struct {
	max  int
	used int
}

func (b *attemptBudget) tryAcquire() bool {
	if b == nil {
		return true
	}
	if b.used >= b.max {
		return false
	}
	b.used++
	return true
}

func (e *Executor) effectiveMaxAttempts() int {
	if e == nil || e.MaxAttempts <= 0 {
		return 3
	}
	return e.MaxAttempts
}

var deterministicNow = time.Unix(1715620000, 0).UTC()

type lockedRng struct {
	mu   sync.Mutex
	base routing.Rng
}

func (l *lockedRng) Intn(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.base.Intn(n)
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return deterministicNow
}

// WallClock returns the configured Now callback or nil (satisfies pkg/lipsdk.ExecutorView).
func (e *Executor) WallClock() func() time.Time {
	if e == nil {
		return nil
	}
	return e.Now
}

func (e *Executor) rng() routing.Rng {
	if e.Rand != nil {
		e.rngOnce.Do(func() {
			e.lockedRand = &lockedRng{base: e.Rand}
		})
		return e.lockedRand
	}
	return rand.New(rand.NewSource(1))
}

func (e *Executor) mergePlannerHealth() map[string]struct{} {
	if e == nil || e.CandidateHealth == nil {
		return nil
	}
	return e.CandidateHealth.UnhealthyCandidateKeys()
}

func (e *Executor) traceRoute(traceID, decision, detail string) {
	if e == nil || e.RouteTrace == nil {
		return
	}
	e.RouteTrace.Append(diag.RouteTraceEntry{TraceID: traceID, Decision: decision, Detail: detail})
}

func (e *Executor) observeRoute(ctx context.Context, traceID, decision, detail string) {
	if e == nil || e.RouteObserver == nil {
		return
	}
	e.RouteObserver.ObserveRouteDecision(ctx, traceID, decision, detail)
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
func (e *Executor) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	if e == nil || e.Store == nil || call == nil {
		return nil, fmt.Errorf("executor: invalid arguments")
	}
	bus := e.Bus
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	if err := call.Validate(); err != nil {
		return nil, err
	}
	traceID, baseline, aLeg, ctx, err := e.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		return nil, err
	}
	sel, err := routing.Parse(baseline.Route.Selector)
	if err != nil {
		return nil, err
	}
	routing.ApplyModelOnlyBackends(sel, e.DefaultBackend)
	if routing.SelectorHasEmptyBackend(sel) {
		return nil, fmt.Errorf("executor: %w", lipapi.ErrUnresolvedModelOnlySelector)
	}
	budget := &attemptBudget{max: e.effectiveMaxAttempts(), used: 0}
	session := &routing.SessionRoutingState{FirstRequestConsumed: aLeg.WeightedFirstConsumed}
	excluded := map[string]struct{}{}
	var lastReject lipapi.NegotiationResult

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		list, err := routing.ExpandFailover(sel, routing.PlanOptions{
			Excluded:  excluded,
			Unhealthy: e.mergePlannerHealth(),
			Session:   session,
			Rand:      e.rng(),
		})
		if err != nil {
			if errors.Is(err, routing.ErrNoEligibleCandidate) && lastReject.Kind == lipapi.NegotiationReject {
				return nil, lastReject.Err()
			}
			if errors.Is(err, routing.ErrNoEligibleCandidate) {
				return nil, err
			}
			return nil, err
		}
		c := list[0]
		e.traceRoute(traceID, "plan_candidate", c.Key)
		e.observeRoute(ctx, traceID, "plan_candidate", c.Key)
		attempt := lipapi.CloneCall(baseline)
		req := lipapi.RequiredCapabilities(attempt)
		be, ok := e.Backends[c.Primary.Backend]
		if !ok {
			return nil, fmt.Errorf("executor: unknown backend %q", c.Primary.Backend)
		}
		res := lipapi.Negotiate(req, e.capsForAttempt(ctx, be, attempt, c))
		if res.Kind == lipapi.NegotiationReject {
			lastReject = res
			diag.LogDecision(ctx, e.Log, "capability_reject", diag.AttrOpts{CallID: traceID},
				slog.String("decision", "exclude_candidate"),
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			excluded[c.Key] = struct{}{}
			continue
		}
		lastReject = lipapi.NegotiationResult{}
		if res.Kind == lipapi.NegotiationDowngrade {
			diag.LogDecision(ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: traceID},
				slog.String("candidate_key", c.Key),
				slog.String("backend", c.Primary.Backend),
			)
			lipapi.ApplyNegotiatedDowngrades(&attempt, res)
		}
		if c.MarkedFirst {
			if err := e.Store.SetWeightedFirstConsumed(ctx, aLeg.ALegID, true); err != nil {
				return nil, err
			}
			session.FirstRequestConsumed = true
		}
		if !budget.tryAcquire() {
			return nil, fmt.Errorf("executor: %w", lipapi.ErrMaxRouteAttempts)
		}
		bleg, err := e.Store.NextBLeg(ctx, aLeg.ALegID)
		if err != nil {
			return nil, err
		}
		if err := bus.RunRequestPartHooks(ctx, &attempt, sdk.PartMeta{
			TraceID:    traceID,
			ALegID:     aLeg.ALegID,
			BLegID:     bleg.BLegID,
			AttemptSeq: bleg.Seq,
		}); err != nil {
			return nil, err
		}
		openCall, err := backendCallWithRouteParams(attempt, c)
		if err != nil {
			return nil, fmt.Errorf("executor: %w", err)
		}
		stream, err := be.Open(ctx, openCall, c)
		if err != nil {
			if lipapi.IsRecoverablePreOutput(err) {
				_ = e.recordAttempt(ctx, aLeg.ALegID, bleg, c, lipapi.AttemptSwallowedFailure, "recoverable pre-output (open)")
				diag.LogDecision(ctx, e.Log, "recoverable_pre_output_swallowed", diag.AttrOpts{CallID: traceID, BLegID: bleg.BLegID},
					slog.String("candidate_key", c.Key),
					slog.String("phase", "open"),
				)
				excluded[c.Key] = struct{}{}
				continue
			}
			_ = e.recordAttempt(ctx, aLeg.ALegID, bleg, c, lipapi.AttemptSurfacedFailure, err.Error())
			return nil, err
		}
		diag.LogDecision(ctx, e.Log, "backend_stream_opened", diag.AttrOpts{CallID: traceID, BLegID: bleg.BLegID},
			slog.String("candidate_key", c.Key),
			slog.String("backend", c.Primary.Backend),
			slog.String("model", c.Primary.Model),
		)
		return &retryRecvStream{
			executor: e,
			bus:      bus,
			baseline: baseline,
			budget:   budget,
			aLegID:   aLeg.ALegID,
			traceID:  traceID,
			sel:      sel,
			session:  session,
			excluded: excluded,
			rng:      e.rng(),
			inner:    stream,
			bleg:     bleg,
			cand:     c,
		}, nil
	}
}

func backendCallWithRouteParams(work lipapi.Call, cand routing.AttemptCandidate) (lipapi.Call, error) {
	merged, err := lipapi.MergeRouteQueryIntoGenerationOptions(work.Options, cand.Primary.Params)
	if err != nil {
		return lipapi.Call{}, fmt.Errorf("route generation options: %w", err)
	}
	work.Options = merged
	return work, nil
}

func (e *Executor) recordAttempt(ctx context.Context, aLegID string, bleg b2bua.BLegRecord, cand routing.AttemptCandidate, out lipapi.AttemptOutcome, reason string) error {
	now := e.now()
	rec := lipapi.AttemptRecord{
		BLegID:         bleg.BLegID,
		ALegID:         aLegID,
		Seq:            bleg.Seq,
		BackendID:      cand.Primary.Backend,
		EffectiveModel: cand.Primary.Model,
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        out,
		Reason:         reason,
	}
	if sink, ok := e.CandidateHealth.(policy.RoutingAttemptOutcomeSink); ok {
		sink.OnRoutingAttemptOutcome(cand.Key, out)
	}
	// Store mutations must not be skipped when the request context is already canceled.
	return e.Store.RecordAttempt(context.WithoutCancel(ctx), rec)
}
