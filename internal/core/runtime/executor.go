package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

var _ lipsdk.ExecutorView = (*Executor)(nil)

// Executor orchestrates hooks, capability negotiation, routing, B2BUA, and backend attempts.
type Executor struct {
	Store b2bua.Store
	Bus   *hooks.Bus
	// RuntimeSnapshot is the per-build execution binding published on each request context.
	// When non-nil, it must not be mutated after the executor is handed to concurrent callers;
	// see [extensions.RequestRuntimeSnapshot].
	RuntimeSnapshot *extensions.RequestRuntimeSnapshot
	Backends        map[string]execbackend.Backend // key: routing.Primary.Backend (non-empty)
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
	// SelectorAliases rewrites the request route selector before routing.Parse (nil or empty rules: no-op).
	SelectorAliases *routing.AliasResolver
	// CapsResolver, when set, supplies candidate-aware caps for negotiation; otherwise each
	// Backend's ResolveCaps / Caps is used via [execbackend.EffectiveCaps].
	CapsResolver capabilities.Resolver
	// CandidateHealth, when set, supplies unhealthy routing keys merged into planner options.
	CandidateHealth policy.CandidateHealth
	// RouteObserver, when set, receives coarse routing decisions (non-blocking contract).
	RouteObserver lipsdk.RouteObserver
	// RouteTrace, when set, records recent routing decisions for diagnostics HTTP handlers.
	RouteTrace *diag.RouteTraceBuffer
	// MaxPendingWireEvents caps backend pending event queues per stream (0 = unlimited).
	MaxPendingWireEvents int
	// Metrics receives coarse executor observations when non-nil.
	Metrics MetricsSink
	// ExtensionMetrics records extension pipeline stage timings when non-nil (Prometheus when enabled).
	ExtensionMetrics extensions.StageMetrics
	// CompletionBufferLimits overrides completion-gate buffering bounds (tests). Zero MaxEvents uses SDK defaults.
	CompletionBufferLimits completion.BufferLimits

	rngOnce    sync.Once
	lockedRand routing.Rng // lazy: mutex-serialized view of Rand
}

func (e *Executor) capsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt lipapi.Call,
	c routing.AttemptCandidate,
) lipapi.BackendCaps {
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, c, attempt)
	}
	return execbackend.EffectiveCaps(ctx, be, attempt, c)
}

// Execute runs submit hooks, resolves the A-leg, plans routes, negotiates per attempt,
// and returns a stream. Recoverable pre-output failures may consume additional B-legs
// before the returned stream yields events.
//
// ctx must be non-nil (same contract as [lipapi.EventStream.Recv]); nil returns [lipapi.ErrNilContext].
const otelScopeExecutor = "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"

func (e *Executor) Execute(ctx context.Context, call *lipapi.Call) (_ lipapi.EventStream, err error) {
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
	if e.RuntimeSnapshot != nil {
		ctx = extensions.WithRequestRuntimeSnapshot(ctx, e.RuntimeSnapshot)
	}
	ctx, execSpan := otel.Tracer(otelScopeExecutor).Start(ctx, "lip.executor.execute")
	defer func() {
		if err != nil {
			execSpan.RecordError(err)
			execSpan.SetStatus(codes.Error, err.Error())
		}
		execSpan.End()
	}()
	traceID, baseline, aLeg, ctx, err := e.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		return nil, fmt.Errorf("executor: prepare submit: %w", err)
	}
	var recvViews execctx.Views
	recvViewsOK := false
	if v, ok := execctx.FromContext(ctx); ok {
		recvViews = v
		recvViewsOK = true
	}
	routePrefs := slices.Clone(execctx.RouteCandidatePreferences(ctx))
	selStr := strings.TrimSpace(baseline.Route.Selector)
	if e.SelectorAliases != nil {
		selStr = e.SelectorAliases.Resolve(selStr)
	}
	sel, err := routing.Parse(selStr)
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

			recvViews:   recvViews,
			recvViewsOK: recvViewsOK,
			routePrefs:  routePrefs,
		}
		rs.storeInner(out.stream)
		return rs, nil
	}
}
