package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func quantitiesFromUsageEvent(ev lipapi.Event) []metering.Quantity {
	totalPresent := ev.TotalTokens > 0 || (ev.InputTokens+ev.OutputTokens) > 0 || ev.Kind == lipapi.EventUsageDelta
	total := int64(ev.TotalTokens)
	if total == 0 {
		total = int64(ev.InputTokens + ev.OutputTokens)
	}
	return checkpoint.QuantitiesFromTokenCounts(
		int64(ev.InputTokens),
		int64(ev.OutputTokens),
		int64(ev.CacheReadTokens),
		int64(ev.CacheWriteTokens),
		int64(ev.ReasoningTokens),
		total,
		totalPresent,
	)
}

func moneyFromUsageEvent(ev lipapi.Event) *metering.MoneyObservation {
	if strings.TrimSpace(ev.Currency) == "" && ev.CostNanoUnits == 0 && strings.TrimSpace(ev.CostSource) == "" {
		return nil
	}
	return &metering.MoneyObservation{
		NanoUnits: ev.CostNanoUnits,
		Currency:  strings.TrimSpace(ev.Currency),
		Present:   true,
		Source:    metering.SourceProviderReported,
	}
}

func (s *retryRecvStream) emitBackendEgressMeteringFact(ctx context.Context, outcome metering.AttemptOutcome, surfaced metering.SurfacedState, usageEv lipapi.Event) {
	if s == nil || s.executor == nil {
		return
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		return
	}
	beSnap := holder.BackendIngressFor(s.bleg.BLegID)
	if beSnap == nil {
		// No freeze available (estimate-only path); skip journal emit.
		return
	}
	seq := holder.NextSequence()
	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.BackendEgressCheckpoint(*beSnap, outcome, surfaced),
		FactID:     fmt.Sprintf("be-egress:%s:%d", strings.TrimSpace(s.bleg.BLegID), seq),
		Sequence:   seq,
		Quantities: quantitiesFromUsageEvent(usageEv),
		Outcome:    outcome,
		Surfaced:   surfaced,
		Money:      moneyFromUsageEvent(usageEv),
		Now:        s.executor.now(),
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "metering backend egress fact", "error", err)
		}
		return
	}
	if err := s.executor.appendMeteringFact(ctx, fact); err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "metering recorder append", "error", err)
	}
}

func (s *retryRecvStream) emitFrontendEgressMeteringFact(ctx context.Context, usageEv lipapi.Event) {
	if s == nil || s.executor == nil {
		return
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil || holder.FrontendIngress == nil {
		return
	}
	seq := holder.NextSequence()
	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.FrontendEgressCheckpoint(*holder.FrontendIngress),
		FactID:     fmt.Sprintf("fe-egress:%s:%d", strings.TrimSpace(s.traceID), seq),
		Sequence:   seq,
		Quantities: quantitiesFromUsageEvent(usageEv),
		Money:      moneyFromUsageEvent(usageEv),
		Now:        s.executor.now(),
	})
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "metering frontend egress fact", "error", err)
		}
		return
	}
	if err := s.executor.appendMeteringFact(ctx, fact); err != nil && s.executor.Log != nil {
		s.executor.Log.DebugContext(ctx, "metering recorder append", "error", err)
	}
}
