package runtimehost

import (
	"net/http"
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
	closed      atomic.Bool
	drainMu     sync.Mutex
	drainCh     chan struct{}
	drainClosed bool

	// retireMu serializes LifecycleWorker.Retire for this generation only.
	// It lives on the generation so concurrent unrelated retirements progress
	// independently without a process-wide lock or unbounded worker map.
	retireMu sync.Mutex

	closeCount atomic.Int32
	closeErr   error

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
		label:   label,
		drainCh: make(chan struct{}),
		owned:   owned,
		meta:    GenerationMeta{Label: label},
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

// CloseCount returns how many times owned teardown successfully claimed
// (published Close or unpublished Discard).
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

// AttachOwned binds generation-owned resources once while preparing/prepared.
func (g *Generation) AttachOwned(owned OwnedCloser) error {
	if g == nil {
		return ErrNotPrepared
	}
	st := g.Lifecycle()
	if st != GenPreparing && st != GenPrepared {
		return ErrIllegalTransition
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	if g.owned != nil || g.requestPlane != nil {
		return ErrOwnedAlreadyBound
	}
	g.owned = owned
	return nil
}

// AttachRequestPlane atomically binds the immutable request-plane publisher as
// both the served plane and the generation-owned closer while preparing.
func (g *Generation) AttachRequestPlane(plane PublishedRequestPlane) error {
	if g == nil {
		return ErrNotPrepared
	}
	st := g.Lifecycle()
	if st != GenPreparing && st != GenPrepared {
		return ErrIllegalTransition
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	if g.requestPlane != nil {
		return ErrRequestPlaneAlreadyBound
	}
	if g.owned != nil {
		return ErrOwnedAlreadyBound
	}
	g.requestPlane = plane
	g.owned = plane
	return nil
}

// RequestPlane returns the bound immutable request-plane publisher, or nil.
func (g *Generation) RequestPlane() PublishedRequestPlane {
	if g == nil {
		return nil
	}
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	return g.requestPlane
}

// Handler returns the bound request-plane handler, or nil when unbound.
func (g *Generation) Handler() http.Handler {
	plane := g.RequestPlane()
	if plane == nil {
		return nil
	}
	return plane.Handler()
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

func (g *Generation) tryRetain() bool {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenActive || refs == ^uint32(0) {
			return false
		}
		if g.word.CompareAndSwap(cur, packLease(st, refs+1)) {
			return true
		}
	}
}

// tryRetainWhileBound increments ownership for a child pin while a request lease
// (or transferred pin path) already proves the generation is still live.
// Active and post-retirement drain states with outstanding refs are allowed so
// a publication race cannot close the generation between child-pin acquisition
// and use. New acquires after drain/close fail closed.
func (g *Generation) tryRetainWhileBound() bool {
	if g == nil {
		return false
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if !childRetainable(st) || refs == 0 || refs == ^uint32(0) {
			return false
		}
		if g.word.CompareAndSwap(cur, packLease(st, refs+1)) {
			return true
		}
	}
}

func childRetainable(st GenLifecycle) bool {
	switch st {
	case GenActive, GenRetiring, GenQuiescing, GenQuiesced:
		return true
	default:
		return false
	}
}

func (g *Generation) releaseRef() {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if refs == 0 {
			return
		}
		nextRefs := refs - 1
		if !g.word.CompareAndSwap(cur, packLease(st, nextRefs)) {
			continue
		}
		if nextRefs == 0 && drainable(st) {
			g.signalDrained()
		}
		return
	}
}

// drainable reports whether last-ref release (or markRetiring with refs=0) may
// transition to GenDrained and close Drained(). GenQuiescing is intentionally
// excluded: quiesce work must finish via MarkQuiesced before drain/close.
func drainable(st GenLifecycle) bool {
	return st == GenRetiring || st == GenQuiesced
}

func (g *Generation) markRetiring() {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenActive {
			return
		}
		if g.word.CompareAndSwap(cur, packLease(GenRetiring, refs)) {
			if refs == 0 {
				g.signalDrained()
			}
			return
		}
	}
}

func (g *Generation) signalDrained() {
	g.drainMu.Lock()
	defer g.drainMu.Unlock()
	if g.drainClosed {
		return
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if refs != 0 {
			return
		}
		if st == GenDrained {
			break
		}
		if !drainable(st) {
			return
		}
		if g.word.CompareAndSwap(cur, packLease(GenDrained, 0)) {
			break
		}
	}
	g.drainClosed = true
	close(g.drainCh)
}

// Close closes generation-owned resources exactly once from Closing (req 10.6).
func (g *Generation) Close() error {
	if g == nil {
		return nil
	}
	if g.closed.Load() {
		return ErrAlreadyClosed
	}
	st, _ := unpackLease(g.word.Load())
	if st != GenClosing {
		return ErrIllegalTransition
	}
	if !g.closed.CompareAndSwap(false, true) {
		return ErrAlreadyClosed
	}
	g.closeCount.Add(1)

	g.payloadMu.Lock()
	owned := g.owned
	g.owned = nil
	g.payloadMu.Unlock()
	if owned != nil {
		g.closeErr = owned.Close()
	}
	for {
		cur := g.word.Load()
		_, refs := unpackLease(cur)
		if g.word.CompareAndSwap(cur, packLease(GenClosed, refs)) {
			break
		}
	}
	if g.closeErr != nil {
		return g.closeErr
	}
	return nil
}

// Discard rolls back an unpublished candidate (preparing/prepared/failed).
// It closes generation-owned resources exactly once and ends in GenFailed
// (req 10.9). It never uses the published drain→BeginClose→Close path and
// never touches process services.
func (g *Generation) Discard() error {
	if g == nil {
		return nil
	}
	if g.closed.Load() {
		return ErrAlreadyClosed
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		switch st {
		case GenPreparing, GenPrepared:
			if !g.word.CompareAndSwap(cur, packLease(GenFailed, refs)) {
				continue
			}
		case GenFailed:
			// already terminal; still claim owned close below
		default:
			return ErrIllegalTransition
		}
		break
	}
	if !g.closed.CompareAndSwap(false, true) {
		return ErrAlreadyClosed
	}
	g.closeCount.Add(1)

	g.payloadMu.Lock()
	owned := g.owned
	g.owned = nil
	g.payloadMu.Unlock()
	if owned != nil {
		g.closeErr = owned.Close()
	}
	if g.closeErr != nil {
		return g.closeErr
	}
	return nil
}

func (g *Generation) assignPublish(id, prev int64, publishedAt time.Time) error {
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
