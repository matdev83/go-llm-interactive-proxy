package runtimehost

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrRetentionBlocked: publish would exceed retained-generation budget (req 10.8-10.11).
var ErrRetentionBlocked = errors.New("runtimehost: retained-generation budget exhausted")

// ErrNotPrepared: nil/unprepared Publish candidate.
var ErrNotPrepared = errors.New("runtimehost: candidate not prepared")

// GenLifecycle is the observable generation lifecycle (req 10.1).
type GenLifecycle uint32

const (
	GenPreparing GenLifecycle = iota
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

// ManualClock is a fake clock for tests (no timing sleeps).
type ManualClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewManualClock(t time.Time) *ManualClock { return &ManualClock{t: t} }
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// packed word: high 32 = lifecycle, low 32 = refcount (req 10.4, 15.1).
// sync.WaitGroup is intentionally not used as a request refcounter.
func packLease(state GenLifecycle, refs uint32) uint64 {
	return (uint64(state) << 32) | uint64(refs)
}
func unpackLease(w uint64) (GenLifecycle, uint32) {
	return GenLifecycle(w >> 32), uint32(w)
}

// RefGeneration is one request-plane generation under the reference manager.
type RefGeneration struct {
	ID          int64
	Label       string
	word        atomic.Uint64
	closed      atomic.Bool
	drainMu     sync.Mutex
	drainCh     chan struct{}
	drainClosed bool
	closeCount  atomic.Int32
}

func newRefGeneration(id int64, label string, state GenLifecycle) *RefGeneration {
	g := &RefGeneration{ID: id, Label: label, drainCh: make(chan struct{})}
	g.word.Store(packLease(state, 0))
	return g
}
func (g *RefGeneration) Lifecycle() GenLifecycle {
	st, _ := unpackLease(g.word.Load())
	return st
}
func (g *RefGeneration) Refs() uint32 {
	_, refs := unpackLease(g.word.Load())
	return refs
}
func (g *RefGeneration) Drained() <-chan struct{} { return g.drainCh }
func (g *RefGeneration) CloseCount() int32        { return g.closeCount.Load() }
func (g *RefGeneration) tryRetain() bool {
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
func (g *RefGeneration) releaseRef() {
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if refs == 0 {
			return
		}
		if !g.word.CompareAndSwap(cur, packLease(st, refs-1)) {
			continue
		}
		if st == GenRetiring && refs-1 == 0 {
			g.signalDrained()
		}
		return
	}
}
func (g *RefGeneration) markRetiring() {
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
func (g *RefGeneration) signalDrained() {
	g.drainMu.Lock()
	defer g.drainMu.Unlock()
	if g.drainClosed {
		return
	}
	g.drainClosed = true
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenRetiring || refs != 0 {
			break
		}
		if g.word.CompareAndSwap(cur, packLease(GenDrained, 0)) {
			break
		}
	}
	close(g.drainCh)
}

// Close closes generation-owned resources exactly once (req 10.6).
func (g *RefGeneration) Close() {
	if !g.closed.CompareAndSwap(false, true) {
		return
	}
	g.closeCount.Add(1)
	for {
		cur := g.word.Load()
		st, _ := unpackLease(cur)
		if st == GenClosed {
			return
		}
		if g.word.CompareAndSwap(cur, packLease(GenClosed, 0)) {
			return
		}
	}
}

// RefLease is a hot-path request lease (req 5.3-5.4, 15.1).
type RefLease struct {
	gen         *RefGeneration
	released    atomic.Bool
	transferred atomic.Bool
}

func (l *RefLease) Generation() *RefGeneration {
	if l == nil {
		return nil
	}
	return l.gen
}

// Release drops the lease retain exactly once; double-close is a no-op (req 10.4).
func (l *RefLease) Release() {
	if l == nil || !l.released.CompareAndSwap(false, true) || l.transferred.Load() {
		return
	}
	l.gen.releaseRef()
}

// TransferPin converts the lease retain into an async/SSE/provider pin (req 5.7, 10.3).
func (l *RefLease) TransferPin(kind PinKind) (*RefPin, bool) {
	if l == nil || !l.released.CompareAndSwap(false, true) {
		return nil, false
	}
	l.transferred.Store(true)
	return &RefPin{gen: l.gen, kind: kind}, true
}

// RefPin retains a generation across SSE/async/provider work.
type RefPin struct {
	gen      *RefGeneration
	kind     PinKind
	released atomic.Bool
}

func (p *RefPin) Kind() PinKind {
	if p == nil {
		return 0
	}
	return p.kind
}
func (p *RefPin) Generation() *RefGeneration {
	if p == nil {
		return nil
	}
	return p.gen
}
func (p *RefPin) Release() {
	if p == nil || !p.released.CompareAndSwap(false, true) {
		return
	}
	p.gen.releaseRef()
}

// RefGenerationManager is the task 1.4 contract reference model (not phase-3 production).
type RefGenerationManager struct {
	active          atomic.Pointer[RefGeneration]
	nextID          atomic.Int64
	maxRetained     int
	mu              sync.Mutex
	retained        []*RefGeneration
	clock           *ManualClock
	afterRetainHook atomic.Value // func(*RefGeneration)
}

// NewRefGenerationManager constructs a reference manager with a finite retained-
// generation budget (req 10.8). clock may be nil.
func NewRefGenerationManager(maxRetained int, clock *ManualClock) *RefGenerationManager {
	if maxRetained < 0 {
		maxRetained = 0
	}
	return &RefGenerationManager{maxRetained: maxRetained, clock: clock}
}
func (m *RefGenerationManager) PrepareCandidate(label string) *RefGeneration {
	return newRefGeneration(0, label, GenPreparing)
}
func (m *RefGenerationManager) Active() *RefGeneration { return m.active.Load() }
func (m *RefGenerationManager) RetainedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.retained)
}
func (m *RefGenerationManager) ClockNow() time.Time {
	if m.clock == nil {
		return time.Time{}
	}
	return m.clock.Now()
}

// SetAfterRetainHook installs a barrier hook between tryRetain and pointer recheck.
func (m *RefGenerationManager) SetAfterRetainHook(fn func(*RefGeneration)) {
	if fn == nil {
		m.afterRetainHook.Store(func(*RefGeneration) {})
		return
	}
	m.afterRetainHook.Store(fn)
}

// Acquire loads the active generation with pointer recheck (req 5.3-5.4, 10.2, 15.1).
func (m *RefGenerationManager) Acquire() (*RefLease, bool) {
	for {
		g := m.active.Load()
		if g == nil {
			return nil, false
		}
		if !g.tryRetain() {
			if m.active.Load() == g {
				return nil, false
			}
			continue
		}
		if v := m.afterRetainHook.Load(); v != nil {
			if hook, ok := v.(func(*RefGeneration)); ok && hook != nil {
				hook(g)
			}
		}
		if m.active.Load() == g {
			return &RefLease{gen: g}, true
		}
		g.releaseRef()
	}
}

// Publish atomically swaps the active pointer after budget reservation (req 5.2, 5.9, 15.4).
func (m *RefGenerationManager) Publish(candidate *RefGeneration) error {
	if candidate == nil {
		return ErrNotPrepared
	}
	st := candidate.Lifecycle()
	if st != GenPreparing && st != GenActive {
		return ErrNotPrepared
	}
	m.mu.Lock()
	if len(m.retained) >= m.maxRetained && m.active.Load() != nil {
		m.mu.Unlock()
		return ErrRetentionBlocked
	}
	candidate.ID = m.nextID.Add(1)
	candidate.word.Store(packLease(GenActive, 0))
	prior := m.active.Swap(candidate)
	if prior != nil {
		prior.markRetiring()
		m.retained = append(m.retained, prior)
	}
	m.mu.Unlock()
	return nil
}

// SweepClosed drops closed generations from the retained budget set.
func (m *RefGenerationManager) SweepClosed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	dst := m.retained[:0]
	for _, g := range m.retained {
		if g.Lifecycle() != GenClosed {
			dst = append(dst, g)
		}
	}
	m.retained = dst
}
