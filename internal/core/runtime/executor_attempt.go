package runtime

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/policy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// recordAttemptParams is the argument bundle for [Executor.recordAttempt].
type recordAttemptParams struct {
	ALegID  string
	BLeg    b2bua.BLegRecord
	Cand    routing.AttemptCandidate
	Outcome lipapi.AttemptOutcome
	Reason  string
}

func (e *Executor) recordAttempt(ctx context.Context, p recordAttemptParams) error {
	now := e.now()
	rec := lipapi.AttemptRecord{
		BLegID:         p.BLeg.BLegID,
		ALegID:         p.ALegID,
		Seq:            p.BLeg.Seq,
		BackendID:      p.Cand.Primary.Backend,
		EffectiveModel: p.Cand.Primary.Model,
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        p.Outcome,
		Reason:         p.Reason,
	}
	if sink, ok := e.CandidateHealth.(policy.RoutingAttemptOutcomeSink); ok {
		sink.OnRoutingAttemptOutcome(p.Cand.Key, p.Outcome)
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
