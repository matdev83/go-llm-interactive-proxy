package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// routePlanState holds routing setup computed once per Execute after request
// preparation and before the initial attempt-open loop.
type routePlanState struct {
	sel                    *routing.Selector
	budget                 *attemptBudget
	ttft                   ttftBudget
	session                *routing.SessionRoutingState
	excluded               map[string]struct{}
	requestSize            routing.RequestSizeEstimate
	affinityKey            affinity.Key
	affinitySet            bool
	interleaved            interleavedstate.State
	rng                    routing.Rng
	failoverReq            capabilities.FailoverRequirementSet
	lastReject             lipapi.NegotiationResult
	lastTransportReject    lipapi.TransportNegotiationResult
	lastAdmissionErr       error
	contextLimitExhaustion bool
	lastParallelFailure    error
	transformExcludes      transformExcludeTracker
}

// buildRoutePlan parses the route selector, applies model-only defaulting, and
// initializes attempt budgets, session routing state, affinity identity, and
// interleaved preload for the initial open loop.
func (e *Executor) buildRoutePlan(ctx context.Context, prep *preparedRequest) (*routePlanState, error) {
	e.noteSelectorAuthority(ctx, prep.traceID, prep.routeAuth)
	sel, err := routing.CompileSelector(prep.baseline.Route.Selector, e.SelectorAliases, e.DefaultBackend)
	if err != nil {
		if errors.Is(err, lipapi.ErrUnresolvedModelOnlySelector) {
			return nil, fmt.Errorf("executor: %w", err)
		}
		return nil, fmt.Errorf("executor: parse route selector: %w", err)
	}
	if err := routing.ValidateExecutionComposition(sel, e.BackendExecutionResolver, e.ExecutionCompositionPolicy); err != nil {
		return nil, fmt.Errorf("executor: %w", err)
	}
	// Bind-time registry view: set NativeModel on every leaf without rewriting
	// Primary.Model so catalog/affinity/traces keep logical identity (req 9.4, 9.10).
	// Wrong-backend canonical leaves fail closed with a typed error.
	if resolver, ok := routing.NativeModelResolverFromContext(ctx); ok {
		if err := routing.BindNativeModelIDs(sel, resolver); err != nil {
			return nil, fmt.Errorf("executor: bind native model ids: %w", err)
		}
	}
	affinityKey, affinityKeyOK, err := e.resolveAffinityKey(sel, prep.recvViews, prep.recvViewsOK)
	if err != nil {
		return nil, fmt.Errorf("executor: affinity identity: %w", err)
	}
	interleaved, err := e.loadInterleavedState(ctx, prep.aLeg.ALegID)
	if err != nil {
		return nil, fmt.Errorf("executor: load interleaved state: %w", err)
	}
	return &routePlanState{
		sel:         sel,
		budget:      &attemptBudget{max: e.effectiveMaxAttempts(), used: 0},
		ttft:        newTTFTBudget(e.now(), sel),
		session:     &routing.SessionRoutingState{FirstRequestConsumed: prep.aLeg.WeightedFirstConsumed},
		excluded:    map[string]struct{}{},
		requestSize: e.requestSizeEstimateForRouting(ctx, sel, prep.baseline),
		affinityKey: affinityKey,
		affinitySet: affinityKeyOK,
		interleaved: interleaved,
		rng:         e.rng(),
		failoverReq: capabilities.NewFailoverRequirementSet(prep.baseline),
	}, nil
}
