package runtime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

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
	return e.Store.RecordAttempt(context.WithoutCancel(ctx), rec)
}

func backendCallWithRouteParams(work lipapi.Call, cand routing.AttemptCandidate) (lipapi.Call, error) {
	merged, err := lipapi.MergeRouteQueryIntoGenerationOptions(work.Options, cand.Primary.Params)
	if err != nil {
		return lipapi.Call{}, fmt.Errorf("route generation options: %w", err)
	}
	work.Options = merged
	return work, nil
}
