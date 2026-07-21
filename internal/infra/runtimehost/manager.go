package runtimehost

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Manager is the production generation publication and acquire surface (req 5.2-5.4, 10, 15).
type Manager struct {
	active          atomic.Pointer[Generation]
	nextID          atomic.Int64
	maxRetained     int
	mu              sync.Mutex
	retained        []*Generation
	clock           *ManualClock
	afterRetainHook atomic.Value // func(*Generation)
}

// NewManager constructs a manager with a finite retained-generation budget (req 10.8).
// clock may be nil.
func NewManager(maxRetained int, clock *ManualClock) *Manager {
	if maxRetained < 0 {
		maxRetained = 0
	}
	return &Manager{maxRetained: maxRetained, clock: clock}
}

// Prepare creates a prepared candidate with no generation-owned payload.
func (m *Manager) Prepare(label string) *Generation {
	return newGeneration(label, GenPrepared, nil)
}

// PrepareOwned creates a prepared candidate bound to generation-owned resources.
func (m *Manager) PrepareOwned(label string, owned OwnedCloser) *Generation {
	return newGeneration(label, GenPrepared, owned)
}

// BeginPrepare creates a preparing candidate (MarkPrepared before Publish).
func (m *Manager) BeginPrepare(label string, owned OwnedCloser) *Generation {
	return newGeneration(label, GenPreparing, owned)
}

// Active returns the current active generation pointer (may be nil).
func (m *Manager) Active() *Generation { return m.active.Load() }

// RetainedCount returns how many retired generations occupy the retention budget.
func (m *Manager) RetainedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.retained)
}

// ClockNow returns the manager clock instant, or zero if unset.
func (m *Manager) ClockNow() time.Time {
	if m.clock == nil {
		return time.Time{}
	}
	return m.clock.Now()
}

// SetAfterRetainHook installs a barrier hook between tryRetain and pointer recheck.
func (m *Manager) SetAfterRetainHook(fn func(*Generation)) {
	if fn == nil {
		m.afterRetainHook.Store((func(*Generation))(nil))
		return
	}
	m.afterRetainHook.Store(fn)
}

// Acquire loads the active generation with retain and pointer recheck (req 5.3-5.4, 10.2, 15.1).
func (m *Manager) Acquire() (*Lease, bool) {
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
			if hook, ok := v.(func(*Generation)); ok && hook != nil {
				hook(g)
			}
		}
		if m.active.Load() == g {
			return &Lease{gen: g}, true
		}
		g.releaseRef()
	}
}

// Publish atomically swaps the active pointer after budget reservation (req 5.2, 5.9, 15.4).
// Retention rejection rolls back the unpublished candidate before returning (req 10.9).
func (m *Manager) Publish(candidate *Generation) error {
	if candidate == nil {
		return ErrNotPrepared
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	switch candidate.Lifecycle() {
	case GenPrepared:
		// ok
	case GenPreparing:
		return ErrNotPrepared
	case GenActive, GenRetiring, GenQuiescing, GenQuiesced, GenDrained, GenClosing, GenClosed:
		return ErrAlreadyPublished
	default:
		return ErrNotPrepared
	}

	if len(m.retained) >= m.maxRetained && m.active.Load() != nil {
		cleanupErr := candidate.Discard()
		if cleanupErr != nil && !errors.Is(cleanupErr, ErrAlreadyClosed) {
			return errors.Join(ErrRetentionBlocked, cleanupErr)
		}
		return ErrRetentionBlocked
	}

	prev := m.active.Load()
	var prevID int64
	if prev != nil {
		prevID = prev.ID()
	}
	id := m.nextID.Add(1)
	publishedAt := time.Now().UTC()
	if m.clock != nil {
		publishedAt = m.clock.Now()
	}
	if err := candidate.assignPublish(id, prevID, publishedAt); err != nil {
		return err
	}

	prior := m.active.Swap(candidate)
	if prior != nil {
		prior.markRetiring()
		m.retained = append(m.retained, prior)
	}
	return nil
}

// SweepClosed drops closed generations from the retained budget set.
func (m *Manager) SweepClosed() {
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
