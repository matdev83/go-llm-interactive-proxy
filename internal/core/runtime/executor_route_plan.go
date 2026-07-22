package runtime

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
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
	lastReject             lipapi.NegotiationResult
	lastTransportReject    lipapi.TransportNegotiationResult
	contextLimitExhaustion bool
	lastParallelFailure    error
	transformExcludes      transformExcludeTracker
}

// buildRoutePlan parses the route selector, applies model-only defaulting, and
// initializes attempt budgets, session routing state, affinity identity, and
// interleaved preload for the initial open loop.
func (e *Executor) buildRoutePlan(prep *preparedRequest) (*routePlanState, error) {
	selStr := strings.TrimSpace(prep.baseline.Route.Selector)
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
	// Bind-time registry view: set NativeModel on every leaf without rewriting
	// Primary.Model so catalog/affinity/traces keep logical identity (req 9.4, 9.10).
	// Wrong-backend canonical leaves fail closed with a typed error.
	if resolver, ok := routing.NativeModelResolverFromContext(prep.ctx); ok {
		if err := routing.BindNativeModelIDs(sel, resolver); err != nil {
			return nil, fmt.Errorf("executor: bind native model ids: %w", err)
		}
	}
	affinityKey, affinityKeyOK, err := e.resolveAffinityKey(sel, prep.recvViews, prep.recvViewsOK)
	if err != nil {
		return nil, fmt.Errorf("executor: affinity identity: %w", err)
	}
	interleaved, err := e.loadInterleavedState(prep.ctx, prep.aLeg.ALegID)
	if err != nil {
		return nil, fmt.Errorf("executor: load interleaved state: %w", err)
	}
	return &routePlanState{
		sel:         sel,
		budget:      &attemptBudget{max: e.effectiveMaxAttempts(), used: 0},
		ttft:        newTTFTBudget(e.now(), sel),
		session:     &routing.SessionRoutingState{FirstRequestConsumed: prep.aLeg.WeightedFirstConsumed},
		excluded:    map[string]struct{}{},
		requestSize: e.requestSizeEstimateForRouting(prep.ctx, sel, prep.baseline),
		affinityKey: affinityKey,
		affinitySet: affinityKeyOK,
		interleaved: interleaved,
		rng:         e.rng(),
	}, nil
}
