package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// billingTurnCollector owns A-leg evidence, the parallel barrier, TUR persist,
// and detached handoff retry. Runtime streams only RecordLeg / SealTurn.
type billingTurnCollector struct {
	exec *Executor

	mu             sync.Mutex
	evidenceByALeg map[string][]billing.LegUsageRecord
	barrierByALeg  map[string]<-chan struct{}
	sealedByALeg   map[string]struct{}

	retryMu     sync.Mutex
	retryByALeg map[string]struct{}
	retryWG     sync.WaitGroup
	stopOnce    sync.Once
	stopCh      chan struct{}

	finalizeMu    sync.Mutex
	finalizeByKey map[string]*finalizeCacheEntry
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
	return c != nil && c.exec != nil && (c.exec.BillingTerminalHandoff != nil || c.exec.BillingLegObserver != nil)
}

func (c *billingTurnCollector) record(ctx context.Context, record billing.LegUsageRecord) {
	if c == nil || c.exec == nil {
		return
	}
	aLegID := strings.TrimSpace(record.ALegID)
	if c.exec.BillingTerminalHandoff != nil && aLegID != "" {
		c.mu.Lock()
		if c.evidenceByALeg == nil {
			c.evidenceByALeg = make(map[string][]billing.LegUsageRecord)
		}
		c.evidenceByALeg[aLegID] = mergeBillingEvidence(c.evidenceByALeg[aLegID], []billing.LegUsageRecord{record})
		c.mu.Unlock()
	}
	if c.exec.BillingLegObserver != nil {
		_ = safety.Call(safety.BoundaryStream, "billing_leg_observer", func() error {
			c.exec.BillingLegObserver.ObserveBillingLeg(ctx, record)
			return nil
		})
	}
}

func (c *billingTurnCollector) claim(aLegID string) []billing.LegUsageRecord {
	if c == nil {
		return nil
	}
	aLegID = strings.TrimSpace(aLegID)
	c.mu.Lock()
	defer c.mu.Unlock()
	legs := c.evidenceByALeg[aLegID]
	if len(legs) == 0 {
		return nil
	}
	out := append([]billing.LegUsageRecord(nil), legs...)
	delete(c.evidenceByALeg, aLegID)
	return out
}

func (c *billingTurnCollector) restore(aLegID string, legs []billing.LegUsageRecord) {
	if c == nil || len(legs) == 0 {
		return
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.evidenceByALeg == nil {
		c.evidenceByALeg = make(map[string][]billing.LegUsageRecord)
	}
	c.evidenceByALeg[aLegID] = mergeBillingEvidence(c.evidenceByALeg[aLegID], legs)
}

func (c *billingTurnCollector) peek(aLegID string) []billing.LegUsageRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]billing.LegUsageRecord(nil), c.evidenceByALeg[strings.TrimSpace(aLegID)]...)
}

func (c *billingTurnCollector) beginBarrier(aLegID string) (complete func()) {
	if c == nil {
		return func() {}
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return func() {}
	}
	ch := make(chan struct{})
	c.mu.Lock()
	if c.barrierByALeg == nil {
		c.barrierByALeg = make(map[string]<-chan struct{})
	}
	if _, exists := c.barrierByALeg[aLegID]; exists {
		c.mu.Unlock()
		return func() {}
	}
	c.barrierByALeg[aLegID] = ch
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.barrierByALeg[aLegID] == ch {
				delete(c.barrierByALeg, aLegID)
			}
			c.mu.Unlock()
			close(ch)
		})
	}
}

func (c *billingTurnCollector) waitBarrier(ctx context.Context, aLegID string) bool {
	if c == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	ch := c.barrierByALeg[strings.TrimSpace(aLegID)]
	c.mu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *billingTurnCollector) markSealed(aLegID string) {
	if c == nil {
		return
	}
	aLegID = strings.TrimSpace(aLegID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealedByALeg == nil {
		c.sealedByALeg = make(map[string]struct{})
	}
	c.sealedByALeg[aLegID] = struct{}{}
}

func (c *billingTurnCollector) forgetSealed(aLegID string) {
	if c == nil {
		return
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return
	}
	c.mu.Lock()
	delete(c.sealedByALeg, aLegID)
	c.mu.Unlock()
}

func (c *billingTurnCollector) retryInFlight(aLegID string) bool {
	if c == nil {
		return false
	}
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	_, ok := c.retryByALeg[strings.TrimSpace(aLegID)]
	return ok
}

func (c *billingTurnCollector) forgetSealedIfNoRetry(aLegID string) {
	if c.retryInFlight(aLegID) {
		return
	}
	c.forgetSealed(aLegID)
}

func (c *billingTurnCollector) sealed(aLegID string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.sealedByALeg[strings.TrimSpace(aLegID)]
	return ok
}

// sealTurn persists one TUR from the request terminal owner. It returns true
// only after durable accept. Barrier timeout and persist failure schedule retry.
func (c *billingTurnCollector) sealTurn(ctx context.Context, job billingHandoffRetryJob) bool {
	if c == nil || c.exec == nil || c.exec.BillingTerminalHandoff == nil || !isBillingTurnTerminalCommand(job.command) {
		return false
	}
	if c.sealed(job.aLegID) {
		return true
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	if !c.waitBarrier(persistCtx, job.aLegID) {
		if c.exec.Log != nil {
			c.exec.Log.DebugContext(persistCtx, "billing TUR handoff deferred: parallel evidence barrier incomplete")
		}
		c.scheduleRetry(job)
		return false
	}
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer writeCancel()
	if err := c.persist(writeCtx, job); err != nil {
		if errors.Is(err, errBillingHandoffNoEvidence) {
			if c.exec.Log != nil {
				c.exec.Log.DebugContext(writeCtx, "billing TUR handoff deferred: no B-leg evidence")
			}
			c.scheduleRetry(job)
			return false
		}
		if c.exec.Log != nil {
			c.exec.Log.DebugContext(writeCtx, "billing TUR handoff failed", "error", err)
		}
		c.scheduleRetry(job)
		return false
	}
	c.markSealed(job.aLegID)
	c.forgetSealedIfNoRetry(job.aLegID)
	return true
}

func (c *billingTurnCollector) persist(ctx context.Context, job billingHandoffRetryJob) error {
	if c == nil || c.exec == nil || c.exec.BillingTerminalHandoff == nil {
		return fmt.Errorf("runtime: billing handoff unavailable")
	}
	legs := sealableBillingLegs(c.peek(job.aLegID))
	sort.SliceStable(legs, func(i, j int) bool { return legs[i].Seq < legs[j].Seq })
	if len(legs) == 0 {
		return errBillingHandoffNoEvidence
	}
	started, finished := legs[0].StartedAt, legs[0].FinishedAt
	for _, leg := range legs[1:] {
		if !leg.StartedAt.IsZero() && (started.IsZero() || leg.StartedAt.Before(started)) {
			started = leg.StartedAt
		}
		if leg.FinishedAt.After(finished) {
			finished = leg.FinishedAt
		}
	}
	record := billing.TurnUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		AccountID:          job.accountID,
		TurnID:             job.aLegID,
		ALegID:             job.aLegID,
		AuthorizationID:    job.authorizationID,
		SessionID:          strings.TrimSpace(job.sessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            turnOutcomeFromCommand(job.command),
		CustomerPricingRef: job.customerPricing,
		ChargePolicyRef:    job.chargePolicy,
		Legs:               legs,
	}
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	err = safety.Call(safety.BoundaryStream, "billing_turn_handoff", func() error {
		return c.exec.BillingTerminalHandoff.AppendUsageRecord(ctx, sealed)
	})
	if err != nil {
		return err
	}
	c.evictFinalizeCache(job.aLegID, legs)
	_ = c.claim(job.aLegID)
	return nil
}

func sealableBillingLegs(legs []billing.LegUsageRecord) []billing.LegUsageRecord {
	out := make([]billing.LegUsageRecord, 0, len(legs))
	for _, leg := range legs {
		if leg.StartedAt.IsZero() || leg.FinishedAt.IsZero() || leg.FinishedAt.Before(leg.StartedAt) {
			continue
		}
		out = append(out, leg)
	}
	return out
}

func mergeBillingEvidence(dst, src []billing.LegUsageRecord) []billing.LegUsageRecord {
	if len(src) == 0 {
		return dst
	}
	index := make(map[string]int, len(dst)+len(src))
	for i, leg := range dst {
		key := billingEvidenceDedupeKey(leg)
		index[key] = i
	}
	for _, leg := range src {
		key := billingEvidenceDedupeKey(leg)
		if i, ok := index[key]; ok {
			dst[i] = leg
			continue
		}
		index[key] = len(dst)
		dst = append(dst, leg)
	}
	return dst
}

func billingEvidenceDedupeKey(leg billing.LegUsageRecord) string {
	if id := strings.TrimSpace(leg.BLegID); id != "" {
		return id
	}
	return strings.TrimSpace(leg.ALegID) + "#" + strconv.Itoa(leg.Seq)
}

func (c *billingTurnCollector) releaseHoldAfterExhausted(job billingHandoffRetryJob) {
	if c == nil || c.exec == nil || strings.TrimSpace(job.accountID) == "" || strings.TrimSpace(job.authorizationID) == "" {
		return
	}
	if len(c.peek(job.aLegID)) > 0 {
		return
	}
	releaser := c.exec.BillingHoldReleaser
	if releaser == nil {
		return
	}
	turKey, err := billing.TURKey(job.accountID, job.aLegID)
	if err != nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, releaseErr := releaser.ReleaseAuthorization(releaseCtx, billing.ReleaseAuthorizationInput{
		AccountID: job.accountID, AuthorizationID: job.authorizationID, TURKey: turKey,
		FullClose: true, Reason: billing.ReleaseExecutionNotStarted,
		SourceKey: "handoff_exhausted:" + job.authorizationID,
	}); releaseErr != nil && c.exec.Log != nil {
		c.exec.Log.Debug("billing unused-hold release after handoff exhaustion failed", "a_leg_id", job.aLegID, "error", releaseErr)
	}
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

// finalizeOnce returns one FinalizeBilling snapshot per B-leg. Quota settlement
// and LUR recording share this result; a failed or missing hook is cached as
// a miss so the backend is not retried for the same leg.
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
	if e, ok := c.finalizeByKey[key]; ok {
		c.finalizeMu.Unlock()
		<-e.done
		return e.ev, e.ok
	}
	e := &finalizeCacheEntry{done: make(chan struct{})}
	c.finalizeByKey[key] = e
	c.finalizeMu.Unlock()

	e.ev, e.ok = c.callFinalizeBilling(ctx, in)
	close(e.done)
	return e.ev, e.ok
}

func (c *billingTurnCollector) callFinalizeBilling(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, bool) {
	if c.exec.Backends == nil {
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

// billingHandoffTimeout bounds detached TUR handoff work (barrier wait + persist).
// It must exceed parallel loser cleanup (cancelLosersTimeout) plus per-leg
// FinalizeBilling budgets so client cancellation cannot strand sealed money.
var billingHandoffTimeout = 2 * time.Minute

// billingFinalizeTimeout is the per-leg FinalizeBilling observation budget.
const billingFinalizeTimeout = 2 * time.Second

func (e *Executor) addBillingEvidence(ctx context.Context, record billing.LegUsageRecord) {
	if e == nil {
		return
	}
	e.billingTurns().record(ctx, record)
}

func (e *Executor) claimBillingEvidence(aLegID string) []billing.LegUsageRecord {
	if e == nil {
		return nil
	}
	return e.billingTurns().claim(aLegID)
}

func (e *Executor) restoreBillingEvidence(aLegID string, legs []billing.LegUsageRecord) {
	if e == nil {
		return
	}
	e.billingTurns().restore(aLegID, legs)
}

func (e *Executor) peekBillingEvidence(aLegID string) []billing.LegUsageRecord {
	if e == nil {
		return nil
	}
	return e.billingTurns().peek(aLegID)
}

func (e *Executor) beginBillingEvidenceBarrier(aLegID string) (complete func()) {
	if e == nil {
		return func() {}
	}
	return e.billingTurns().beginBarrier(aLegID)
}

func (e *Executor) waitBillingEvidenceBarrier(ctx context.Context, aLegID string) bool {
	if e == nil {
		return true
	}
	return e.billingTurns().waitBarrier(ctx, aLegID)
}
