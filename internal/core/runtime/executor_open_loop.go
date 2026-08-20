package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

type openNextRequest struct {
	reqFacts    requestFacts
	routeFacts  routeFacts
	progress    *recoveryController
	mode        openMode
	interleaved interleavedstate.State
}

func (e *Executor) openInitialAttempt(ctx context.Context, prep *preparedRequest, plan *routePlanState) (openedAttempt, error) {
	return attemptOpenOwner{e}.openInitial(ctx, prep, plan)
}

// openInitial runs the pre-output attempt-open loop until a backend
// stream is opened and its B-leg is registered with the A-leg scope.
func (o attemptOpenOwner) openInitial(ctx context.Context, prep *preparedRequest, plan *routePlanState) (openedAttempt, error) {
	e := o.Executor
	if prep.recvTurnFacts.billingCallID == "" && prep.billingCallID != "" {
		prep.recvTurnFacts.billingCallID = prep.billingCallID
		prep.recvTurnFacts.billingCallState = prep.billingCallState
	}
	if prep.aLegID == "" && prep.identity != nil {
		prep.recvTurnFacts = newRecvTurnFacts(ctx, recvTurnFactsInput{
			baseline:         *prep.call,
			traceID:          prep.identity.traceID,
			aLegID:           prep.identity.aLeg.ALegID,
			secureTurn:       prep.identity.secureTurn,
			secureTurnOK:     prep.identity.secureTurnOK,
			billingCallID:    prep.billingCallID,
			billingCallState: prep.billingCallState,
		})
	}
	if plan.progress == nil {
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
	for {
		if err := ctx.Err(); err != nil {
			return openedAttempt{}, err
		}
		if err := prep.aScope.Err(); err != nil {
			return openedAttempt{}, err
		}
		out, err := e.openNext(ctx, openNextRequest{
			reqFacts: requestFacts{
				recvTurnFacts: prep.recvTurnFacts,
				bus:           prep.bus,
				aScope:        prep.aScope,
			},
			routeFacts:  plan.facts(),
			progress:    plan.progress,
			mode:        openModeInitial,
			interleaved: plan.progress.interleaved,
		})
		if err != nil {
			return out, err
		}
		if out.session == nil {
			plan.progress.interleaved = out.interleaved
			continue
		}
		return out, nil
	}
}
