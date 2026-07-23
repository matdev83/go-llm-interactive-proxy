package configreload

import (
	"sync"
)

// DefaultStatusHistoryCap is the default bounded ring capacity for reload status history.
const DefaultStatusHistoryCap = 32

// StatusHistory is a process-owned bounded ring of reload history entries.
// Entry shape is the canonical HistoryEntry from pkg/lipsdk/configreload (aliased here).
type StatusHistory struct {
	mu    sync.Mutex
	cap   int
	buf   []HistoryEntry
	head  int
	count int
}

// NewStatusHistory creates a ring with capacity n (defaults to DefaultStatusHistoryCap).
func NewStatusHistory(n int) *StatusHistory {
	if n < 1 {
		n = DefaultStatusHistoryCap
	}
	return &StatusHistory{cap: n, buf: make([]HistoryEntry, n)}
}

// Append records an entry, dropping the oldest when full.
func (h *StatusHistory) Append(e HistoryEntry) {
	if h == nil {
		return
	}
	e.SafeActor = truncateActor(e.SafeActor)
	e.Stage = sanitizeStage(e.Stage)
	e.ReasonCategory = sanitizeStage(e.ReasonCategory)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cap < 1 {
		return
	}
	if len(h.buf) != h.cap {
		h.buf = make([]HistoryEntry, h.cap)
		h.head = 0
		h.count = 0
	}
	if h.count < h.cap {
		idx := (h.head + h.count) % h.cap
		h.buf[idx] = e
		h.count++
		return
	}
	h.head = (h.head + 1) % h.cap
	h.buf[(h.head+h.count-1)%h.cap] = e
}

// Snapshot returns a copy of recent entries (oldest first).
func (h *StatusHistory) Snapshot() []HistoryEntry {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return []HistoryEntry{}
	}
	out := make([]HistoryEntry, h.count)
	for i := 0; i < h.count; i++ {
		out[i] = h.buf[(h.head+i)%h.cap]
	}
	return out
}

func truncateActor(s string) string {
	s = stringsTrim(s)
	if len(s) > 64 {
		s = s[:64]
	}
	if looksSecret(s) {
		return RedactedPlaceholder
	}
	return s
}

func sanitizeStage(s string) string {
	s = stringsTrim(s)
	if s == "" {
		return ""
	}
	if looksSecret(s) {
		return "other"
	}
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
