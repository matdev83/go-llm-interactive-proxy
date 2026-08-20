package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func (e *Executor) billingEnabled() bool {
	return e != nil && (e.BillingLegObserver != nil || e.hasTerminalSink())
}

func (e *Executor) observeBillingLeg(ctx context.Context, record billing.CallLegUsageRecord) {
	if e == nil || e.BillingLegObserver == nil {
		return
	}
	_ = safety.Call(safety.BoundaryStream, "billing_leg_observer", func() error {
		e.BillingLegObserver.ObserveBillingLeg(ctx, record)
		return nil
	})
}

func (e *Executor) callFinalizeBilling(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
	if e == nil || e.Backends == nil {
		return lipapi.Event{}, fmt.Errorf("executor finalizer: no backends")
	}
	backendID := strings.TrimSpace(in.Backend)
	be, ok := e.Backends[backendID]
	if !ok || be.FinalizeBilling == nil {
		return lipapi.Event{}, fmt.Errorf("executor finalizer: backend %q does not support FinalizeBilling", backendID)
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingFinalizeTimeout)
	defer cancel()
	in.Backend = backendID
	ev, err := safety.CallValue(safety.BoundaryBackend, "backend_finalize_billing", func() (lipapi.Event, error) {
		return be.FinalizeBilling(persistCtx, in)
	})
	if err != nil {
		if e.Log != nil {
			e.Log.DebugContext(persistCtx, "billing FinalizeBilling", "error", err)
		}
		return lipapi.Event{}, err
	}
	if ev.Kind != lipapi.EventUsageDelta {
		return lipapi.Event{}, fmt.Errorf("executor finalizer: invalid event kind %q", ev.Kind)
	}
	return ev, nil
}

type billingCallState struct {
	callID billing.BillingCallID

	mu sync.Mutex

	allocated map[string]int // BLegID -> actual AttemptSeq
	frozen    []string
	hasFrozen bool
	legTimes  []billingLegTiming

	finalizeMu sync.Mutex
	finalize   map[string]*finalizeCacheEntry
}

func newBillingCallState(callID billing.BillingCallID) *billingCallState {
	return &billingCallState{
		callID:    callID,
		allocated: make(map[string]int),
		finalize:  make(map[string]*finalizeCacheEntry),
	}
}

func (s *billingCallState) noteAllocatedBLeg(bLegID string, seq int) {
	if s == nil {
		return
	}
	bLegID = strings.TrimSpace(bLegID)
	if bLegID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasFrozen {
		return
	}
	if s.allocated == nil {
		s.allocated = make(map[string]int)
	}
	s.allocated[bLegID] = seq
}

func (s *billingCallState) freezeAllocatedBLegs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasFrozen {
		return append([]string(nil), s.frozen...)
	}
	ids := make([]string, 0, len(s.allocated))
	for id := range s.allocated {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	s.frozen = append([]string(nil), ids...)
	s.hasFrozen = true
	return ids
}

func (s *billingCallState) noteLegTimes(started, finished time.Time) {
	if s == nil || started.IsZero() || finished.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasFrozen {
		return
	}
	s.legTimes = append(s.legTimes, billingLegTiming{startedAt: started, finishedAt: finished})
}

func (s *billingCallState) timingBounds(now time.Time) (time.Time, time.Time) {
	if s == nil {
		return now, now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var started, finished time.Time
	for _, leg := range s.legTimes {
		if !leg.startedAt.IsZero() && (started.IsZero() || leg.startedAt.Before(started)) {
			started = leg.startedAt
		}
		if !leg.finishedAt.IsZero() && (finished.IsZero() || leg.finishedAt.After(finished)) {
			finished = leg.finishedAt
		}
	}
	if started.IsZero() {
		started = now
	}
	if finished.IsZero() {
		finished = now
	}
	if finished.Before(started) {
		finished = started
	}
	return started, finished
}

type billingLegTiming struct {
	startedAt  time.Time
	finishedAt time.Time
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

func (s *billingCallState) finalizeOnce(ctx context.Context, in execbackend.BillingFinalizationInput, finalizeFn func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error)) (lipapi.Event, bool) {
	if s == nil {
		return lipapi.Event{}, false
	}
	key := finalizeCacheKey(in)
	if key == "" {
		ev, err := finalizeFn(ctx, in)
		return ev, err == nil && ev.Kind == lipapi.EventUsageDelta
	}

	s.finalizeMu.Lock()
	if s.finalize == nil {
		s.finalize = make(map[string]*finalizeCacheEntry)
	}
	entry, ok := s.finalize[key]
	if ok {
		s.finalizeMu.Unlock()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return lipapi.Event{}, false
		}
		return entry.ev, entry.ok
	}

	entry = &finalizeCacheEntry{done: make(chan struct{})}
	s.finalize[key] = entry
	s.finalizeMu.Unlock()

	defer close(entry.done)
	ev, err := finalizeFn(ctx, in)
	entry.ev = ev
	entry.ok = err == nil && ev.Kind == lipapi.EventUsageDelta

	return entry.ev, entry.ok
}

const billingFinalizeTimeout = 2 * time.Second

var billingHandoffTimeout = 2 * time.Minute
