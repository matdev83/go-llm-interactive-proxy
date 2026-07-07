package runtime

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
)

// openInitialAttempt runs the pre-output attempt-open loop until a backend
// stream is opened and its B-leg is registered with the A-leg scope.
func (e *Executor) openInitialAttempt(prep *preparedRequest, plan *routePlanState) (attemptOpenResult, error) {
	for {
		if err := prep.ctx.Err(); err != nil {
			return attemptOpenResult{}, err
		}
		if err := prep.aScope.Err(); err != nil {
			return attemptOpenResult{}, err
		}
		out, err := e.tryPlanOpenOnce(attemptOpenParams{
			ctx:                      prep.ctx,
			bus:                      prep.bus,
			traceID:                  prep.traceID,
			aLegID:                   prep.aLeg.ALegID,
			aScope:                   prep.aScope,
			baseline:                 prep.baseline,
			sel:                      plan.sel,
			requestSize:              plan.requestSize,
			session:                  plan.session,
			excluded:                 plan.excluded,
			rng:                      plan.rng,
			budget:                   plan.budget,
			ttft:                     &plan.ttft,
			isRetryPath:              false,
			lastReject:               &plan.lastReject,
			lastTransportReject:      &plan.lastTransportReject,
			lastParallelFailure:      &plan.lastParallelFailure,
			affinityKey:              plan.affinityKey,
			affinitySet:              plan.affinitySet,
			isContextLimitExhaustion: &plan.contextLimitExhaustion,
			interleaved:              plan.interleaved,
		})
		if err != nil {
			return attemptOpenResult{}, fmt.Errorf("executor: plan or open attempt: %w", err)
		}
		if !out.opened {
			plan.interleaved = out.interleaved
			continue
		}
		if !out.registered {
			if err := prep.aScope.RegisterBLeg(prep.ctx, leglifecycle.BLegHandle{
				ID:      out.bleg.BLegID,
				Attempt: lifecycleAttempt(out.stream),
			}); err != nil {
				if out.stream != nil && !errors.Is(err, leglifecycle.ErrALegCanceled) {
					_ = out.stream.Close()
				}
				return attemptOpenResult{}, err
			}
		}
		return out, nil
	}
}
