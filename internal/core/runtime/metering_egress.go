package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func appendPresentQuantity(out []metering.Quantity, include bool, component, unit string, value int64) []metering.Quantity {
	if !include {
		return out
	}
	return append(out, metering.Quantity{
		Component: component, Unit: unit, Value: value, Present: true,
	})
}

func quantitiesFromUsageEvent(ev lipapi.Event) []metering.Quantity {
	p := ev.UsagePresence
	if p.Any() {
		var out []metering.Quantity
		out = appendPresentQuantity(out, p.InputTokens, metering.ComponentInputToken, metering.UnitToken, int64(ev.InputTokens))
		out = appendPresentQuantity(out, p.OutputTokens, metering.ComponentOutputToken, metering.UnitToken, int64(ev.OutputTokens))
		out = appendPresentQuantity(out, p.CacheReadTokens, metering.ComponentCacheReadInputToken, metering.UnitToken, int64(ev.CacheReadTokens))
		out = appendPresentQuantity(out, p.CacheWriteTokens, metering.ComponentCacheWriteInputToken, metering.UnitToken, int64(ev.CacheWriteTokens))
		out = appendPresentQuantity(out, p.ReasoningTokens, metering.ComponentReasoningOutputToken, metering.UnitToken, int64(ev.ReasoningTokens))
		out = appendPresentQuantity(out, p.TotalTokens, metering.ComponentTotalToken, metering.UnitToken, int64(ev.TotalTokens))
		return out
	}
	if ev.Kind != lipapi.EventUsageDelta {
		return nil
	}
	// Legacy unmarked UsageDelta: only nonzero counters are present. All-zero
	// events stay omitted (unknown/absent), never Present:true zeros.
	var out []metering.Quantity
	out = appendPresentQuantity(out, ev.InputTokens != 0, metering.ComponentInputToken, metering.UnitToken, int64(ev.InputTokens))
	out = appendPresentQuantity(out, ev.OutputTokens != 0, metering.ComponentOutputToken, metering.UnitToken, int64(ev.OutputTokens))
	out = appendPresentQuantity(out, ev.CacheReadTokens != 0, metering.ComponentCacheReadInputToken, metering.UnitToken, int64(ev.CacheReadTokens))
	out = appendPresentQuantity(out, ev.CacheWriteTokens != 0, metering.ComponentCacheWriteInputToken, metering.UnitToken, int64(ev.CacheWriteTokens))
	out = appendPresentQuantity(out, ev.ReasoningTokens != 0, metering.ComponentReasoningOutputToken, metering.UnitToken, int64(ev.ReasoningTokens))
	out = appendPresentQuantity(out, ev.TotalTokens != 0, metering.ComponentTotalToken, metering.UnitToken, int64(ev.TotalTokens))
	if ev.TotalTokens == 0 && len(out) > 0 {
		derived := int64(ev.InputTokens + ev.OutputTokens)
		out = appendPresentQuantity(out, derived != 0, metering.ComponentTotalToken, metering.UnitToken, derived)
	}
	return out
}

func moneyFromUsageEvent(ev lipapi.Event) *metering.MoneyObservation {
	if !ev.CostPresent {
		return nil
	}
	return &metering.MoneyObservation{
		NanoUnits: ev.CostNanoUnits,
		Currency:  strings.TrimSpace(ev.Currency),
		Present:   true,
		Source:    metering.SourceProviderReported,
	}
}

// emitBackendEgressMeteringFact appends a backend-egress fact when a freeze exists
// for the B-leg (requirements 2.3, 5.3). Nil recorder / missing freeze are no-ops.
func (e *Executor) emitBackendEgressMeteringFact(
	ctx context.Context,
	blegID string,
	outcome metering.AttemptOutcome,
	surfaced metering.SurfacedState,
	usageEv lipapi.Event,
) {
	if e == nil {
		return
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		return
	}
	beSnap := holder.BackendIngressFor(blegID)
	if beSnap == nil {
		return
	}
	seq := holder.NextSequence()
	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.BackendEgressCheckpoint(*beSnap, outcome, surfaced),
		FactID:     fmt.Sprintf("be-egress:%s:%d", strings.TrimSpace(blegID), seq),
		Sequence:   seq,
		Quantities: quantitiesFromUsageEvent(usageEv),
		Outcome:    outcome,
		Surfaced:   surfaced,
		Money:      moneyFromUsageEvent(usageEv),
		Now:        e.now(),
	})
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "metering backend egress fact", "error", err)
		}
		return
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil && e.Log != nil {
		e.Log.DebugContext(ctx, "metering recorder append", "error", err)
	}
}

// emitFrontendEgressMeteringFact appends a frontend-egress fact when FE ingress exists
// (requirement 2.4). Safe to call from winner finalize, cancel-after-output, and
// client-visible error terminals. Returns the emitted fact for request-authority
// settlement (requirement 4.2).
func (e *Executor) emitFrontendEgressMeteringFact(ctx context.Context, traceID string, usageEv lipapi.Event) (metering.Fact, bool) {
	if e == nil {
		return metering.Fact{}, false
	}
	holder := meteringHolderFrom(ctx)
	if holder == nil || holder.FrontendIngress == nil {
		return metering.Fact{}, false
	}
	customerEv := customerPlaneUsageEvent(usageEv)
	if customerEv.Kind == "" && usageEv.Kind == lipapi.EventUsageDelta {
		// Provider-only evidence still requires a customer FE terminal fact with
		// quantities omitted rather than importing provider counters/money.
		customerEv = lipapi.Event{Kind: lipapi.EventUsageDelta}
	}
	seq := holder.NextSequence()
	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.FrontendEgressCheckpoint(*holder.FrontendIngress),
		FactID:     fmt.Sprintf("fe-egress:%s:%d", strings.TrimSpace(traceID), seq),
		Sequence:   seq,
		Quantities: quantitiesFromUsageEvent(customerEv),
		Money:      nil,
		Now:        e.now(),
	})
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "metering frontend egress fact", "error", err)
		}
		return metering.Fact{}, false
	}
	if err := e.appendMeteringFact(ctx, fact); err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "metering recorder append", "error", err)
		}
		return metering.Fact{}, false
	}
	return fact, true
}

func (s *retryRecvStream) emitBackendEgressMeteringFact(ctx context.Context, outcome metering.AttemptOutcome, surfaced metering.SurfacedState, usageEv lipapi.Event) {
	if s == nil || s.executor == nil {
		return
	}
	s.executor.emitBackendEgressMeteringFact(ctx, s.bleg.BLegID, outcome, surfaced, usageEv)
}

func (s *retryRecvStream) emitFrontendEgressMeteringFact(ctx context.Context, usageEv lipapi.Event) (metering.Fact, bool) {
	if s == nil || s.executor == nil {
		return metering.Fact{}, false
	}
	return s.executor.emitFrontendEgressMeteringFact(ctx, s.traceID, s.resolveCustomerUsage(ctx, usageEv))
}

func (s *retryRecvStream) usageEvidenceOrEmpty() lipapi.Event {
	if s == nil {
		return lipapi.Event{}
	}
	ev := authorityUsageEvent(tokenAccountingUsageEvents(s.seenEvents))
	if ev.Kind != "" {
		return ev
	}
	if s.lastAuthorityUsage.Kind != "" {
		return s.lastAuthorityUsage
	}
	return lipapi.Event{Kind: lipapi.EventUsageDelta}
}
