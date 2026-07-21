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
	shuttingDown    atomic.Bool
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

// PrepareRequestPlane creates a prepared candidate bound to an immutable
// request-plane publisher (owned close + Handler).
func (m *Manager) PrepareRequestPlane(label string, plane PublishedRequestPlane) *Generation {
	return newGenerationWithRequestPlane(label, GenPrepared, plane)
}

// BeginPrepare creates a preparing candidate (MarkPrepared before Publish).
func (m *Manager) BeginPrepare(label string, owned OwnedCloser) *Generation {
	return newGeneration(label, GenPreparing, owned)
}

// BeginPrepareRequestPlane creates a preparing candidate bound to an immutable
// request-plane publisher (MarkPrepared before Publish).
func (m *Manager) BeginPrepareRequestPlane(label string, plane PublishedRequestPlane) *Generation {
	return newGenerationWithRequestPlane(label, GenPreparing, plane)
}

// Active returns the current active generation pointer (may be nil).
func (m *Manager) Active() *Generation { return m.active.Load() }

// RetainedCount returns how many retired generations occupy the retention budget.
func (m *Manager) RetainedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.retained)
}

// HasOpenGenerations reports whether any active or retained generation is still
// non-closed. Used by process shutdown to avoid closing ProcessServices while a
// generation pin/lease remains (req 13.x).
func (m *Manager) HasOpenGenerations() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.active.Load(); g != nil && g.Lifecycle() != GenClosed {
		return true
	}
	for _, g := range m.retained {
		if g != nil && g.Lifecycle() != GenClosed {
			return true
		}
	}
	return false
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
	if m == nil || m.shuttingDown.Load() {
		return nil, false
	}
	for {
		if m.shuttingDown.Load() {
			return nil, false
		}
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
		if !m.shuttingDown.Load() && m.active.Load() == g {
			return &Lease{gen: g}, true
		}
		g.releaseRef()
		if m.shuttingDown.Load() {
			return nil, false
		}
	}
}

// Publish atomically swaps the active pointer after budget reservation (req 5.2, 5.9, 15.4).
// Retention rejection and host-shutdown rejection roll back the unpublished candidate
// exactly once after releasing Manager.mu so closers may re-enter status paths (req 10.9).
func (m *Manager) Publish(candidate *Generation) error {
	if candidate == nil {
		return ErrNotPrepared
	}

	m.mu.Lock()
	var reject error
	switch {
	case m.shuttingDown.Load():
		reject = ErrHostShuttingDown
	default:
		switch candidate.Lifecycle() {
		case GenPrepared:
			// ok
		case GenPreparing:
			m.mu.Unlock()
			return ErrNotPrepared
		case GenActive, GenRetiring, GenQuiescing, GenQuiesced, GenDrained, GenClosing, GenClosed:
			m.mu.Unlock()
			return ErrAlreadyPublished
		default:
			m.mu.Unlock()
			return ErrNotPrepared
		}

		if len(m.retained) >= m.maxRetained && m.active.Load() != nil {
			reject = ErrRetentionBlocked
		} else {
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
				m.mu.Unlock()
				return err
			}

			prior := m.active.Swap(candidate)
			if prior != nil {
				prior.markRetiring()
				m.retained = append(m.retained, prior)
			}
			m.mu.Unlock()
			return nil
		}
	}
	m.mu.Unlock()

	cleanupErr := candidate.Discard()
	if cleanupErr != nil && !errors.Is(cleanupErr, ErrAlreadyClosed) {
		return errors.Join(reject, cleanupErr)
	}
	return reject
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
