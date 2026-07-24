package runtimehost

import (
	"sync"
	"sync/atomic"
	"time"
)

// GenLifecycle is the observable generation lifecycle (req 10.1).
type GenLifecycle uint32

const (
	GenPreparing GenLifecycle = iota
	GenPrepared
	GenActive
	GenRetiring
	GenQuiescing
	GenQuiesced
	GenDrained
	GenClosing
	GenClosed
	GenFailed
)

// PinKind classifies transferable pins (req 5.7, 10.3).
type PinKind uint8

const (
	PinHTTP PinKind = iota
	PinSSE
	PinAsync
	PinProvider
)

// OwnedCloser is the narrow generation-owned teardown surface.
// Process services must never be passed here (req 4.9, 6.x).
type OwnedCloser interface {
	Close() error
}

// MetaHints are safe, non-secret metadata supplied before publication (req 4.2, 4.8).
type MetaHints struct {
	PublicFingerprint string
	TriggerKind       string
	LoadedAt          time.Time
}

// GenerationMeta is stable public generation metadata (req 4.2, 4.8).
// It never carries private digests, mutable config, or mixed Built handles.
type GenerationMeta struct {
	ID                int64
	InstanceID        string // opaque Manager process incarnation (task 3.6)
	PreviousID        int64
	Label             string
	PublicFingerprint string
	TriggerKind       string
	LoadedAt          time.Time
	PublishedAt       time.Time
}

// Status is an immutable snapshot of generation state for diagnostics (req 4.8).
type Status struct {
	Meta      GenerationMeta
	Lifecycle GenLifecycle
	Refs      uint32
}

// packed word: high 32 = lifecycle, low 32 = refcount (req 10.4, 15.1).
// Request/lease refcounting uses this packed atomic word, not a wait-group.
func packLease(state GenLifecycle, refs uint32) uint64 {
	return (uint64(state) << 32) | uint64(refs)
}

func unpackLease(w uint64) (GenLifecycle, uint32) {
	return GenLifecycle(w >> 32), uint32(w)
}

// Generation is one immutable request-plane generation under the manager.
type Generation struct {
	id          atomic.Int64
	label       string
	word        atomic.Uint64
	drainMu     sync.Mutex
	drainCh     chan struct{}
	drainClosed bool

	// retireAdmit serializes retirement attempts (Manager.RetireGeneration /
	// scheduled auto-retire) for this generation only, context-aware so a
	// background retirement blocked on a pin cannot make a context-bounded
	// caller (e.g. ShutdownDetached) block forever. It lives on the
	// generation so concurrent unrelated retirements progress independently
	// without a process-wide lock or unbounded worker map.
	retireAdmit retireAdmission

	closeCount atomic.Int32
	closeMu    sync.Mutex // serializes Closing→Closed / Discard cleanup attempts (retry-safe)

	metaMu sync.RWMutex
	meta   GenerationMeta

	// payloadMu keeps the served request plane and its owned closer an atomic
	// ownership pair. A generation must never serve plane A while closing B.
	payloadMu    sync.Mutex
	owned        OwnedCloser
	requestPlane PublishedRequestPlane
}

func newGeneration(label string, state GenLifecycle, owned OwnedCloser) *Generation {
	g := &Generation{
		label:       label,
		drainCh:     make(chan struct{}),
		owned:       owned,
		meta:        GenerationMeta{Label: label},
		retireAdmit: newRetireAdmission(),
	}
	g.word.Store(packLease(state, 0))
	return g
}

func newGenerationWithRequestPlane(label string, state GenLifecycle, plane PublishedRequestPlane) *Generation {
	g := newGeneration(label, state, plane)
	g.requestPlane = plane
	return g
}

// ID returns the process-local generation id (0 until published).
func (g *Generation) ID() int64 {
	if g == nil {
		return 0
	}
	return g.id.Load()
}

// Label returns the preparation label (safe public tag).
func (g *Generation) Label() string {
	if g == nil {
		return ""
	}
	return g.label
}

// Lifecycle returns the current lifecycle state.
func (g *Generation) Lifecycle() GenLifecycle {
	if g == nil {
		return GenFailed
	}
	st, _ := unpackLease(g.word.Load())
	return st
}

// Refs returns the current lease/pin reference count.
func (g *Generation) Refs() uint32 {
	if g == nil {
		return 0
	}
	_, refs := unpackLease(g.word.Load())
	return refs
}

// Drained is closed exactly once when a drain-eligible state reaches refs=0 (req 10.6).
// GenRetiring and GenQuiesced drain on last-ref release (or markRetiring with refs=0).
// GenQuiescing does not: only MarkQuiesced may drain so quiesce work can finish first.
func (g *Generation) Drained() <-chan struct{} {
	if g == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return g.drainCh
}

// CloseCount returns how many owned-teardown attempts have been made
// (published Close attempts or the claiming unpublished Discard attempt),
// regardless of whether each attempt succeeded.
func (g *Generation) CloseCount() int32 {
	if g == nil {
		return 0
	}
	return g.closeCount.Load()
}

// SetMetaHints stores safe metadata before publication.
func (g *Generation) SetMetaHints(h MetaHints) {
	if g == nil {
		return
	}
	g.metaMu.Lock()
	defer g.metaMu.Unlock()
	g.meta.PublicFingerprint = h.PublicFingerprint
	g.meta.TriggerKind = h.TriggerKind
	g.meta.LoadedAt = h.LoadedAt
}

// Status returns a defensive snapshot (req 4.8).
func (g *Generation) Status() Status {
	if g == nil {
		return Status{}
	}
	g.metaMu.RLock()
	meta := g.meta
	g.metaMu.RUnlock()
	st, refs := unpackLease(g.word.Load())
	meta.ID = g.id.Load()
	meta.Label = g.label
	return Status{Meta: meta, Lifecycle: st, Refs: refs}
}

// MarkPrepared transitions Preparing → Prepared.
func (g *Generation) MarkPrepared() error {
	return g.casLifecycle(GenPreparing, GenPrepared)
}

// BeginQuiesce transitions Retiring → Quiescing (post-publish lifecycle worker).
func (g *Generation) BeginQuiesce() error {
	return g.casLifecycle(GenRetiring, GenQuiescing)
}

// MarkQuiesced transitions Quiescing → Quiesced and drains when refs are zero.
func (g *Generation) MarkQuiesced() error {
	if err := g.casLifecycle(GenQuiescing, GenQuiesced); err != nil {
		return err
	}
	st, refs := unpackLease(g.word.Load())
	if st == GenQuiesced && refs == 0 {
		g.signalDrained()
	}
	return nil
}

// BeginClose transitions Drained → Closing.
func (g *Generation) BeginClose() error {
	return g.casLifecycle(GenDrained, GenClosing)
}

// Transition applies a narrow set of explicit transitions; all others fail.
func (g *Generation) Transition(to GenLifecycle) error {
	if g == nil {
		return ErrNotPrepared
	}
	for {
		cur := g.word.Load()
		from, refs := unpackLease(cur)
		if !legalExplicitTransition(from, to) {
			return ErrIllegalTransition
		}
		if g.word.CompareAndSwap(cur, packLease(to, refs)) {
			return nil
		}
	}
}

func legalExplicitTransition(from, to GenLifecycle) bool {
	switch from {
	case GenPreparing:
		return to == GenPrepared || to == GenFailed
	case GenPrepared:
		return to == GenFailed
	default:
		return false
	}
}

func (g *Generation) casLifecycle(from, to GenLifecycle) error {
	if g == nil {
		return ErrNotPrepared
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != from {
			return ErrIllegalTransition
		}
		if g.word.CompareAndSwap(cur, packLease(to, refs)) {
			return nil
		}
	}
}

func (g *Generation) assignPublish(id, prev int64, publishedAt time.Time) error {
	// Serialize Prepared→Active with Attach* payload binding under payloadMu so
	// no attach can commit after the publication transition.
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenPrepared {
			if st == GenActive || st == GenRetiring || st == GenQuiescing ||
				st == GenQuiesced || st == GenDrained || st == GenClosing || st == GenClosed {
				return ErrAlreadyPublished
			}
			return ErrNotPrepared
		}
		if !g.word.CompareAndSwap(cur, packLease(GenActive, refs)) {
			continue
		}
		break
	}
	g.id.Store(id)
	g.metaMu.Lock()
	g.meta.ID = id
	g.meta.PreviousID = prev
	g.meta.PublishedAt = publishedAt
	g.meta.Label = g.label
	g.metaMu.Unlock()
	return nil
}

// assignPublishWithInstance assigns publish metadata including manager instance ID.
func (g *Generation) assignPublishWithInstance(id, prev int64, instanceID string, publishedAt time.Time) error {
	if err := g.assignPublish(id, prev, publishedAt); err != nil {
		return err
	}
	g.metaMu.Lock()
	g.meta.InstanceID = instanceID
	g.metaMu.Unlock()
	return nil
}
