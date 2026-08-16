package runtime

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type billingTurnCollector struct {
	exec            *Executor
	mu              sync.Mutex
	allocatedByCall map[string]map[string]struct{}
	frozenByCall    map[string][]string
	legTimesByCall  map[string][]billing.LegUsageRecord
	finalizeMu      sync.Mutex
	finalizeByKey   map[string]*finalizeCacheEntry
}

func (e *Executor) billingTurns() *billingTurnCollector {
	if e == nil {
		return nil
	}
	e.billingOnce.Do(func() {
		e.billingColl = &billingTurnCollector{exec: e}
	})
	return e.billingColl
}

func (c *billingTurnCollector) enabled() bool {
	return c != nil && c.exec != nil && (c.exec.BillingLegObserver != nil || c.exec.CallLegUsageAppender != nil || c.exec.CallUsageAppender != nil)
}

func (c *billingTurnCollector) observe(ctx context.Context, record billing.LegUsageRecord) {
	if c == nil || c.exec == nil || c.exec.BillingLegObserver == nil {
		return
	}
	_ = safety.Call(safety.BoundaryStream, "billing_leg_observer", func() error {
		c.exec.BillingLegObserver.ObserveBillingLeg(ctx, record)
		return nil
	})
}

func (c *billingTurnCollector) noteAllocatedBLeg(callID billing.BillingCallID, bLegID string) {
	if c == nil {
		return
	}
	if err := callID.Validate(); err != nil {
		return
	}
	bLegID = strings.TrimSpace(bLegID)
	if bLegID == "" {
		return
	}
	key := callID.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, frozen := c.frozenByCall[key]; frozen {
		return
	}
	if c.allocatedByCall == nil {
		c.allocatedByCall = make(map[string]map[string]struct{})
	}
	set := c.allocatedByCall[key]
	if set == nil {
		set = make(map[string]struct{})
		c.allocatedByCall[key] = set
	}
	set[bLegID] = struct{}{}
}

func (c *billingTurnCollector) noteLegTimes(callID billing.BillingCallID, started, finished time.Time) {
	if c == nil {
		return
	}
	if err := callID.Validate(); err != nil || started.IsZero() || finished.IsZero() {
		return
	}
	key := callID.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, frozen := c.frozenByCall[key]; frozen {
		return
	}
	if c.legTimesByCall == nil {
		c.legTimesByCall = make(map[string][]billing.LegUsageRecord)
	}
	c.legTimesByCall[key] = append(c.legTimesByCall[key], billing.LegUsageRecord{StartedAt: started, FinishedAt: finished})
}

func (c *billingTurnCollector) closureLegTimes(callID billing.BillingCallID) []billing.LegUsageRecord {
	if c == nil {
		return nil
	}
	if err := callID.Validate(); err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]billing.LegUsageRecord(nil), c.legTimesByCall[callID.String()]...)
}

func (c *billingTurnCollector) freezeAllocatedBLegs(callID billing.BillingCallID) []string {
	if c == nil {
		return nil
	}
	if err := callID.Validate(); err != nil {
		return nil
	}
	key := callID.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if frozen, ok := c.frozenByCall[key]; ok {
		return append([]string(nil), frozen...)
	}
	set := c.allocatedByCall[key]
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if c.frozenByCall == nil {
		c.frozenByCall = make(map[string][]string)
	}
	c.frozenByCall[key] = append([]string(nil), ids...)
	return ids
}

type finalizeCacheEntry struct {
	done chan struct{}
	ev   lipapi.Event
	ok   bool
}

func finalizeCacheKey(in execbackend.BillingFinalizationInput) string {
	if id := strings.TrimSpace(in.BLegID); id != "" {
		return id
	}
	return strings.TrimSpace(in.ALegID) + "|" + strings.TrimSpace(in.Backend) + "|" + strings.TrimSpace(in.Model)
}

func (c *billingTurnCollector) finalizeOnce(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, bool) {
	if c == nil || c.exec == nil {
		return lipapi.Event{}, false
	}
	key := finalizeCacheKey(in)
	if key == "" {
		return c.callFinalizeBilling(ctx, in)
	}
	c.finalizeMu.Lock()
	if c.finalizeByKey == nil {
		c.finalizeByKey = make(map[string]*finalizeCacheEntry)
	}
	if entry, ok := c.finalizeByKey[key]; ok {
		c.finalizeMu.Unlock()
		<-entry.done
		return entry.ev, entry.ok
	}
	entry := &finalizeCacheEntry{done: make(chan struct{})}
	c.finalizeByKey[key] = entry
	c.finalizeMu.Unlock()
	entry.ev, entry.ok = c.callFinalizeBilling(ctx, in)
	close(entry.done)
	return entry.ev, entry.ok
}

func (c *billingTurnCollector) callFinalizeBilling(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, bool) {
	if c == nil || c.exec == nil || c.exec.Backends == nil {
		return lipapi.Event{}, false
	}
	backendID := strings.TrimSpace(in.Backend)
	be, ok := c.exec.Backends[backendID]
	if !ok || be.FinalizeBilling == nil {
		return lipapi.Event{}, false
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingFinalizeTimeout)
	defer cancel()
	in.Backend = backendID
	ev, err := be.FinalizeBilling(persistCtx, in)
	if err != nil {
		if c.exec.Log != nil {
			c.exec.Log.DebugContext(persistCtx, "billing FinalizeBilling", "error", err)
		}
		return lipapi.Event{}, false
	}
	if ev.Kind != lipapi.EventUsageDelta {
		return lipapi.Event{}, false
	}
	return ev, true
}

func (c *billingTurnCollector) evictFinalizeCache(aLegID string, legs []billing.LegUsageRecord) {
	if c == nil {
		return
	}
	c.finalizeMu.Lock()
	defer c.finalizeMu.Unlock()
	for _, leg := range legs {
		delete(c.finalizeByKey, finalizeCacheKey(execbackend.BillingFinalizationInput{
			ALegID: aLegID, BLegID: leg.BLegID, Backend: leg.BackendID, Model: leg.ModelID,
		}))
	}
}

const billingFinalizeTimeout = 2 * time.Second

var billingHandoffTimeout = 2 * time.Minute
