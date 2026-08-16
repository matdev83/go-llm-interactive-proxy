package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
)

func (e *Executor) openInitialAttempt(ctx context.Context, prep *preparedRequest, plan *routePlanState) (attemptOpenResult, error) {
	return attemptOpenOwner{e}.openInitial(ctx, prep, plan)
}

// openInitial runs the pre-output attempt-open loop until a backend
// stream is opened and its B-leg is registered with the A-leg scope.
func (o attemptOpenOwner) openInitial(ctx context.Context, prep *preparedRequest, plan *routePlanState) (attemptOpenResult, error) {
	e := o.Executor
	for {
		if err := ctx.Err(); err != nil {
			return attemptOpenResult{}, err
		}
		if err := prep.aScope.Err(); err != nil {
			return attemptOpenResult{}, err
		}
		out, err := e.tryPlanOpenOnce(ctx, attemptOpenParams{
			bus:                      prep.bus,
			traceID:                  prep.traceID,
			aLegID:                   prep.aLeg.ALegID,
			aScope:                   prep.aScope,
			baseline:                 prep.baseline,
			failoverReq:              plan.failoverReq,
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
			lastAdmissionErr:         &plan.lastAdmissionErr,
			lastParallelFailure:      &plan.lastParallelFailure,
			affinityKey:              plan.affinityKey,
			affinitySet:              plan.affinitySet,
			isContextLimitExhaustion: &plan.contextLimitExhaustion,
			transformExcludes:        &plan.transformExcludes,
			interleaved:              plan.interleaved,
			billingCallID:            prep.billingCallID,
		})
		if err != nil {
			return attemptOpenResult{}, fmt.Errorf("executor: plan or open attempt: %w", err)
		}
		if !out.opened {
			plan.interleaved = out.interleaved
			continue
		}
		if !out.registered {
			if err := prep.aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
				ID:      out.bleg.BLegID,
				Attempt: lifecycleAttempt(out.stream),
			}); err != nil {
				if out.stream != nil && !errors.Is(err, leglifecycle.ErrALegCanceled) {
					_ = out.stream.Close()
				}
				// RegisterBLeg failed, so the freshly-admitted reservation carried in
				// out.authority is never stored into a holder field on this path and
				// would otherwise be orphaned. Release it with ReleaseKindSwallowed,
				// mirroring the swallowed-authority release sites in executor_recv_loop.
				l := e.newAttemptAuthorityLifecycle(out.authority, out.cand)
				l.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindSwallowed, emptyOperatorUsageShell())
				// Open succeeded and NextBLeg was noted, but no terminal recorder owns
				// the stream. Emit Failed usage before Execute's abort closure freezes.
				e.appendPostOpenTerminalLeg(ctx, prep.billingCallID, prep.aLeg.ALegID, out.bleg, out.cand.Primary, time.Time{}, time.Time{})
				return attemptOpenResult{}, err
			}
		}
		return out, nil
	}
}
