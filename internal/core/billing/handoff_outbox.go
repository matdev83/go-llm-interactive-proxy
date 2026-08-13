package billing

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// HandoffRetryJob is a detached TUR seal attempt. It carries stamped identity
// and B-leg evidence only — never a live stream or provider SDK type.
type HandoffRetryJob struct {
	AccountID          string
	AuthorizationID    string
	ALegID             string
	SessionID          string
	Outcome            TurnOutcome
	CustomerPricing    VersionRef
	ChargePolicy       VersionRef
	UpstreamOpened     bool
	BarrierPending     bool
	Legs               []LegUsageRecord
	NoEvidenceAttempts int
	EvidenceAttempts   int
}

// HandoffOutbox stores pending TUR handoff jobs for an explicit retry worker.
// Runtime records legs and enqueues identity; it does not own retry goroutines.
type HandoffOutbox interface {
	MergeLegs(context.Context, string, []LegUsageRecord) error
	Enqueue(context.Context, HandoffRetryJob) error
	MarkBarrierPending(context.Context, string) error
	MarkBarrierComplete(context.Context, string) error
	ClaimDue(context.Context, int) ([]HandoffRetryJob, error)
	Complete(context.Context, string) error
	Defer(context.Context, HandoffRetryJob, time.Duration) error
	Pending(context.Context) (int, error)
}

type memoryHandoffEntry struct {
	job       HandoffRetryJob
	next      time.Time
	enqueued  bool
	claimed   bool
	completed bool
}

// MemoryHandoffOutbox is the process-local outbox used by tests and by runtime
// when no durable store is injected.
type MemoryHandoffOutbox struct {
	mu     sync.Mutex
	byALeg map[string]*memoryHandoffEntry
	now    func() time.Time
}

func NewMemoryHandoffOutbox() *MemoryHandoffOutbox {
	return &MemoryHandoffOutbox{byALeg: make(map[string]*memoryHandoffEntry), now: time.Now}
}

func (o *MemoryHandoffOutbox) MergeLegs(_ context.Context, aLegID string, legs []LegUsageRecord) error {
	if o == nil {
		return fmt.Errorf("billing: nil handoff outbox")
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return fmt.Errorf("%w: A-leg ID is required", ErrInvalidRecord)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ent := o.entryLocked(aLegID)
	ent.job.ALegID = aLegID
	ent.job.Legs = mergeHandoffLegs(ent.job.Legs, legs)
	return nil
}

func (o *MemoryHandoffOutbox) Enqueue(_ context.Context, job HandoffRetryJob) error {
	if o == nil {
		return fmt.Errorf("billing: nil handoff outbox")
	}
	job.ALegID = strings.TrimSpace(job.ALegID)
	job.AccountID = strings.TrimSpace(job.AccountID)
	job.AuthorizationID = strings.TrimSpace(job.AuthorizationID)
	if job.ALegID == "" || job.AccountID == "" || job.AuthorizationID == "" {
		return fmt.Errorf("%w: account, authorization, and A-leg identity are required", ErrInvalidRecord)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ent := o.entryLocked(job.ALegID)
	if len(job.Legs) > 0 {
		ent.job.Legs = mergeHandoffLegs(ent.job.Legs, job.Legs)
	}
	job.Legs = ent.job.Legs
	job.NoEvidenceAttempts = ent.job.NoEvidenceAttempts
	job.EvidenceAttempts = ent.job.EvidenceAttempts
	if ent.job.BarrierPending {
		job.BarrierPending = true
	}
	ent.job = job
	ent.enqueued = true
	ent.claimed = false
	ent.completed = false
	ent.next = o.now()
	return nil
}

func (o *MemoryHandoffOutbox) MarkBarrierPending(_ context.Context, aLegID string) error {
	if o == nil {
		return fmt.Errorf("billing: nil handoff outbox")
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return fmt.Errorf("%w: A-leg ID is required", ErrInvalidRecord)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ent := o.entryLocked(aLegID)
	ent.job.ALegID = aLegID
	ent.job.BarrierPending = true
	return nil
}

func (o *MemoryHandoffOutbox) MarkBarrierComplete(_ context.Context, aLegID string) error {
	if o == nil {
		return fmt.Errorf("billing: nil handoff outbox")
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return fmt.Errorf("%w: A-leg ID is required", ErrInvalidRecord)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ent := o.entryLocked(aLegID)
	ent.job.BarrierPending = false
	ent.next = o.now()
	ent.claimed = false
	return nil
}

func (o *MemoryHandoffOutbox) ClaimDue(_ context.Context, limit int) ([]HandoffRetryJob, error) {
	if o == nil {
		return nil, fmt.Errorf("billing: nil handoff outbox")
	}
	if limit <= 0 {
		limit = 32
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	ids := make([]string, 0, len(o.byALeg))
	for id, ent := range o.byALeg {
		if ent == nil || !ent.enqueued || ent.completed || ent.claimed || ent.next.After(now) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]HandoffRetryJob, 0, len(ids))
	for _, id := range ids {
		ent := o.byALeg[id]
		ent.claimed = true
		job := ent.job
		job.Legs = append([]LegUsageRecord(nil), ent.job.Legs...)
		out = append(out, job)
	}
	return out, nil
}

func (o *MemoryHandoffOutbox) Complete(_ context.Context, aLegID string) error {
	if o == nil {
		return fmt.Errorf("billing: nil handoff outbox")
	}
	aLegID = strings.TrimSpace(aLegID)
	o.mu.Lock()
	defer o.mu.Unlock()
	if ent := o.byALeg[aLegID]; ent != nil {
		ent.completed = true
		ent.enqueued = false
		ent.claimed = false
		ent.job.Legs = nil
	}
	return nil
}

func (o *MemoryHandoffOutbox) Defer(_ context.Context, job HandoffRetryJob, after time.Duration) error {
	if o == nil {
		return fmt.Errorf("billing: nil handoff outbox")
	}
	aLegID := strings.TrimSpace(job.ALegID)
	if aLegID == "" {
		return fmt.Errorf("%w: A-leg ID is required", ErrInvalidRecord)
	}
	if after < 0 {
		after = 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ent := o.entryLocked(aLegID)
	ent.job.NoEvidenceAttempts = job.NoEvidenceAttempts
	ent.job.EvidenceAttempts = job.EvidenceAttempts
	ent.job.BarrierPending = job.BarrierPending
	ent.claimed = false
	ent.enqueued = true
	ent.completed = false
	ent.next = o.now().Add(after)
	return nil
}

func (o *MemoryHandoffOutbox) Pending(context.Context) (int, error) {
	if o == nil {
		return 0, fmt.Errorf("billing: nil handoff outbox")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, ent := range o.byALeg {
		if ent != nil && ent.enqueued && !ent.completed {
			n++
		}
	}
	return n, nil
}

func (o *MemoryHandoffOutbox) entryLocked(aLegID string) *memoryHandoffEntry {
	if o.byALeg == nil {
		o.byALeg = make(map[string]*memoryHandoffEntry)
	}
	ent := o.byALeg[aLegID]
	if ent == nil {
		ent = &memoryHandoffEntry{job: HandoffRetryJob{ALegID: aLegID}}
		o.byALeg[aLegID] = ent
	}
	return ent
}

func mergeHandoffLegs(dst, src []LegUsageRecord) []LegUsageRecord {
	if len(src) == 0 {
		return dst
	}
	index := make(map[string]int, len(dst)+len(src))
	for i, leg := range dst {
		index[handoffLegKey(leg)] = i
	}
	for _, leg := range src {
		key := handoffLegKey(leg)
		if i, ok := index[key]; ok {
			dst[i] = leg
			continue
		}
		index[key] = len(dst)
		dst = append(dst, leg)
	}
	return dst
}

func handoffLegKey(leg LegUsageRecord) string {
	if id := strings.TrimSpace(leg.BLegID); id != "" {
		return id
	}
	return strings.TrimSpace(leg.ALegID) + "#" + fmt.Sprintf("%d", leg.Seq)
}
