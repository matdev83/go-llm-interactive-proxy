package openaicodex

import (
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	sessionTurnTTL        = time.Hour
	sessionTurnMaxEntries = 1024
)

// sessionTurnCounter tracks, per session key, which backend round-trips
// ("turns") have been reserved so the openai-codex connector can apply early-
// and mid-session verbosity bumps against a single shared turn number. The key
// prefers the proxy-owned session identity when available, so concurrent
// sessions handled by the same backend instance do not share turn counters. It
// is in-memory, per-process, TTL-bounded, and LRU-capped, mirroring
// wsContinuationStore. A nil receiver is safe: reserve returns 0 and release is
// a no-op.
//
// Turn allocation is atomic under the mutex: reserveTurn assigns a unique turn
// number immediately so concurrent opens for the same session key cannot both
// observe the same turn. Callers must releaseTurn when the open ultimately
// fails so failed attempts do not permanently consume slots.
type sessionTurnCounter struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]sessionTurnEntry
	order      []string
	now        func() time.Time
}

type sessionTurnEntry struct {
	nextTurn  int
	active    map[int]struct{}
	expiresAt time.Time
}

func newSessionTurnCounter(ttl time.Duration, maxEntries int) *sessionTurnCounter {
	if ttl <= 0 {
		ttl = sessionTurnTTL
	}
	if maxEntries <= 0 {
		maxEntries = sessionTurnMaxEntries
	}
	return &sessionTurnCounter{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]sessionTurnEntry),
		now:        time.Now,
	}
}

func verbosityTurnsEnabled(cfg Config) bool {
	return !cfg.EarlySessionVerbosityBumpDisabled || !cfg.MidSessionVerbosityBumpDisabled
}

// currentTurnNumber reports the next turn number that reserveTurn would assign
// without mutating state. Turn 1 is returned for a new key, and 0 is returned
// when the key is empty or the counter is nil.
func (s *sessionTurnCounter) currentTurnNumber(convID string) int {
	if s == nil || strings.TrimSpace(convID) == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry, ok := s.entries[convID]
	if !ok || entry.nextTurn <= 0 {
		return 1
	}
	return entry.nextTurn
}

// reserveTurn allocates the next turn number for the given key under the lock
// and returns it. Empty ids and nil receivers return 0. Callers must releaseTurn on
// failure paths so failed opens do not permanently consume slots.
func (s *sessionTurnCounter) reserveTurn(convID string) int {
	if s == nil || strings.TrimSpace(convID) == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry := s.entries[convID]
	if entry.nextTurn <= 0 {
		entry.nextTurn = 1
	}
	turnNo := entry.nextTurn
	entry.nextTurn++
	if entry.active == nil {
		entry.active = make(map[int]struct{})
	}
	entry.active[turnNo] = struct{}{}
	entry.expiresAt = s.now().Add(s.ttl)
	s.entries[convID] = entry
	s.touchLocked(convID)
	for len(s.entries) > s.maxEntries && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
	return turnNo
}

// releaseTurn undoes a prior reserveTurn for the given key and turn number. It
// is a no-op when the key is unknown, empty, or the turn was already released.
func (s *sessionTurnCounter) releaseTurn(convID string, turnNo int) {
	if s == nil || strings.TrimSpace(convID) == "" || turnNo <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry, ok := s.entries[convID]
	if !ok || entry.nextTurn <= 0 || entry.active == nil {
		return
	}
	if _, ok := entry.active[turnNo]; !ok {
		return
	}
	delete(entry.active, turnNo)
	if len(entry.active) == 0 {
		delete(s.entries, convID)
		out := s.order[:0]
		for _, existing := range s.order {
			if existing != convID {
				out = append(out, existing)
			}
		}
		s.order = out
		return
	}
	if turnNo == entry.nextTurn-1 {
		for entry.nextTurn > 1 {
			if _, ok := entry.active[entry.nextTurn-1]; ok {
				break
			}
			entry.nextTurn--
		}
	}
	entry.expiresAt = s.now().Add(s.ttl)
	s.entries[convID] = entry
	s.touchLocked(convID)
}

func (s *sessionTurnCounter) purgeExpiredLocked() {
	now := s.now()
	out := s.order[:0]
	for _, key := range s.order {
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
			continue
		}
		out = append(out, key)
	}
	s.order = out
}

func (s *sessionTurnCounter) touchLocked(key string) {
	out := s.order[:0]
	for _, existing := range s.order {
		if existing != key {
			out = append(out, existing)
		}
	}
	out = append(out, key)
	s.order = out
}

// sessionTurnKey prefers the stable conversation identity for turn counting,
// then falls back to session correlation and finally to the caller's own id so
// callers without continuity metadata still retain a per-conversation counter.
func sessionTurnKey(call lipapi.Call, fallback string) string {
	if id := strings.TrimSpace(call.Session.ContinuityKey); id != "" {
		return "continuity:" + id
	}
	if id := strings.TrimSpace(call.Session.CorrelationID()); id != "" {
		return "session:" + id
	}
	if id := strings.TrimSpace(call.ID); id != "" && !isGeneratedCallID(id) {
		return "call:" + id
	}
	return fallback
}
