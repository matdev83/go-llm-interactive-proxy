package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/affinity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type candidateFailureHistory struct {
	CapabilityReject  lipapi.NegotiationResult
	TransportReject   lipapi.TransportNegotiationResult
	AdmissionErr      error
	ContextLimit      bool
	TransformExcludes *transformExcludeTracker
	ParallelFailure   error
	progress          *recoveryController
}

func (h *candidateFailureHistory) FinalError(base error) error {
	if h == nil {
		return base
	}
	switch {
	case h.TransportReject.Kind == lipapi.NegotiationReject:
		return h.TransportReject.Err()
	case h.AdmissionErr != nil:
		return h.AdmissionErr
	case h.CapabilityReject.Kind == lipapi.NegotiationReject:
		return h.CapabilityReject.Err()
	case h.ContextLimit:
		return lipapi.ErrAllCandidatesContextLimitExceeded
	case h.TransformExcludes != nil && h.TransformExcludes.allExcludedError() != nil:
		return h.TransformExcludes.allExcludedError()
	case h.ParallelFailure != nil:
		return h.ParallelFailure
	default:
		return base
	}
}

type routeFacts struct {
	sel         *routing.Selector
	requestSize routing.RequestSizeEstimate
	affinityKey affinity.Key
	affinitySet bool
	rng         routing.Rng
	failoverReq capabilities.FailoverRequirementSet
}
type routePlanState struct {
	routeFacts
	progress *recoveryController
}

func (e *Executor) buildRoutePlan(ctx context.Context, prep *preparedRequest) (*routePlanState, error) {
	e.noteSelectorAuthority(ctx, prep.identity.traceID, prep.identity.routeAuth)
	sel, err := routing.CompileSelector(prep.call.Route.Selector, e.SelectorAliases, e.DefaultBackend)
	if err != nil {
		if errors.Is(err, lipapi.ErrUnresolvedModelOnlySelector) {
			return nil, fmt.Errorf("executor: %w", err)
		}
		return nil, fmt.Errorf("executor: parse route selector: %w", err)
	}
	if err := routing.ValidateExecutionComposition(sel, e.BackendExecutionResolver, e.ExecutionCompositionPolicy); err != nil {
		return nil, fmt.Errorf("executor: %w", err)
	}
	// Typed facts are authoritative; resolver is frozen at preparation and
	// projected into context only for compatibility hooks. Never read live
	// context here; missing resolver is supported pass-through.
	if resolver := prep.nativeResolver; resolver != nil {
		if err := routing.BindNativeModelIDs(sel, resolver); err != nil {
			return nil, fmt.Errorf("executor: bind native model ids: %w", err)
		}
	}
	affinityKey, affinityKeyOK, err := e.resolveAffinityKey(sel, prep.recvViews, prep.recvViewsOK)
	if err != nil {
		return nil, fmt.Errorf("executor: affinity identity: %w", err)
	}
	interleaved, err := e.loadInterleavedState(ctx, prep.identity.aLeg.ALegID)
	if err != nil {
		return nil, fmt.Errorf("executor: load interleaved state: %w", err)
	}
	failures := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
	budget := &attemptBudget{
		max:      e.effectiveMaxAttempts(),
		used:     0,
		failures: failures,
	}
	ttft := newTTFTBudget(e.now(), sel)
	sessionState := &routing.SessionRoutingState{FirstRequestConsumed: prep.identity.aLeg.WeightedFirstConsumed}
	excluded := map[string]struct{}{}
	requestSize := e.requestSizeEstimateForRouting(ctx, sel, *prep.call)
	rng := e.rng()
	failoverReq := capabilities.NewFailoverRequirementSet(*prep.call)
	progress := newRecoveryController(recoveryControllerInput{
		e:              e,
		affinityStore:  e.AffinityStore,
		log:            e.Log,
		streamRecovery: e.StreamRecovery,
		opener:         newReplacementOpener(e, prep.bus, prep.aScope),
		budget:         budget,
		ttft:           ttft,
		sel:            sel,
		requestSize:    requestSize,
		session:        sessionState,
		excluded:       excluded,
		rng:            rng,
		affinityKey:    affinityKey,
		affinitySet:    affinityKeyOK,
		interleaved:    interleaved,
	})
	rf := routeFacts{
		sel:         sel,
		requestSize: requestSize,
		affinityKey: affinityKey,
		affinitySet: affinityKeyOK,
		rng:         rng,
		failoverReq: failoverReq,
	}
	return &routePlanState{
		routeFacts: rf,
		progress:   progress,
	}, nil
}

func (p *routePlanState) facts() routeFacts {
	if p == nil {
		return routeFacts{}
	}
	return p.routeFacts
}

func (plan *routePlanState) ensureProgress(e *Executor, prep *preparedRequest) {
	if plan == nil || plan.progress != nil {
		return
	}
	plan.progress = newRecoveryController(recoveryControllerInput{
		e:              e,
		affinityStore:  e.AffinityStore,
		log:            e.Log,
		streamRecovery: e.StreamRecovery,
		opener:         newReplacementOpener(e, prep.bus, prep.aScope),
		budget:         &attemptBudget{max: e.effectiveMaxAttempts()},
		ttft:           newTTFTBudget(e.now(), plan.sel),
		sel:            plan.sel,
		requestSize:    plan.requestSize,
		session:        &routing.SessionRoutingState{FirstRequestConsumed: prep.identity.aLeg.WeightedFirstConsumed},
		excluded:       map[string]struct{}{},
		rng:            plan.rng,
		affinityKey:    plan.affinityKey,
		affinitySet:    plan.affinitySet,
	})
}
