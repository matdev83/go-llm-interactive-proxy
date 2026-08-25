package runtime

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	maxAttemptAccumulatedUsage    = 1024
	maxAttemptUsageDedupeKeyBytes = 4096
)

func (a *attemptSession) rememberUsageEvidenceOnce(ev lipapi.Event) bool {
	if a == nil {
		return false
	}
	if len(ev.Accounting.DedupeKey) > maxAttemptUsageDedupeKeyBytes {
		return false
	}
	key := strings.TrimSpace(ev.Accounting.DedupeKey)
	if key == "" {
		return false
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.internalUsageKeys == nil {
		a.internalUsageKeys = make(map[string]struct{})
	}
	if _, exists := a.internalUsageKeys[key]; exists {
		return false
	}
	if len(a.internalUsageKeys) >= maxAttemptAccumulatedUsage {
		return false
	}
	a.internalUsageKeys[key] = struct{}{}
	a.accumulatedUsage = append(a.accumulatedUsage, ev)
	return true
}

func (a *attemptSession) recordUsageEvidence(ev lipapi.Event) {
	if a == nil || ev.Kind == "" {
		return
	}
	if !a.rememberUsageEvidenceOnce(ev) {
		return
	}
	a.accounting.observeUsage(ev)
}

func (a *attemptSession) aggregatedUsageEvidence() lipapi.Event {
	if a == nil {
		return lipapi.Event{}
	}
	a.usageMu.Lock()
	if len(a.accumulatedUsage) == 0 {
		a.usageMu.Unlock()
		return lipapi.Event{}
	}
	events := append([]lipapi.Event(nil), a.accumulatedUsage...)
	a.usageMu.Unlock()
	return authorityUsageEvent(events)
}

func (a *attemptSession) drainStreamUsageEvidence(inner lipapi.ManagedEventStream) {
	if a == nil || inner == nil {
		return
	}
	source, ok := inner.(lipapi.UsageEvidenceSource)
	if !ok {
		return
	}
	_ = safety.Call(safety.BoundaryBackend, "backend_stream_drain_usage", func() error {
		for _, ev := range source.DrainUsageEvidence() {
			if ev.Kind != lipapi.EventUsageDelta {
				continue
			}
			if a.rememberUsageEvidenceOnce(ev) {
				a.accounting.observeUsage(ev)
			}
		}
		return nil
	})
}

// usageOrAccumulated returns primary when it carries token or cost presence,
// otherwise the best attempt-owned accumulated evidence, else an empty shell.
func (a *attemptSession) usageOrAccumulated(primary lipapi.Event) lipapi.Event {
	if primary.Kind != "" && (primary.UsagePresence.Any() || primary.CostPresent) {
		return primary
	}
	if acc := a.aggregatedUsageEvidence(); acc.Kind != "" {
		return acc
	}
	return emptyOperatorUsageShell()
}

// augmentBillingUsage merges attempt-owned accumulated evidence into a terminal
// billing stream event: full substitution when the stream event lacks presence,
// otherwise provider-cost backfill only.
func (a *attemptSession) augmentBillingUsage(streamEv, fallbackPrimary lipapi.Event) lipapi.Event {
	if streamEv.Kind == "" {
		streamEv = fallbackPrimary
	}
	if accumulatedEv := a.aggregatedUsageEvidence(); accumulatedEv.Kind != "" {
		if streamEv.Kind == "" || (!streamEv.UsagePresence.Any() && !streamEv.CostPresent) {
			streamEv = accumulatedEv
		} else if !streamEv.CostPresent && accumulatedEv.CostPresent {
			streamEv.CostPresent, streamEv.CostNanoUnits, streamEv.Currency = true, accumulatedEv.CostNanoUnits, accumulatedEv.Currency
		}
	}
	if streamEv.Kind == "" {
		return emptyOperatorUsageShell()
	}
	return streamEv
}
