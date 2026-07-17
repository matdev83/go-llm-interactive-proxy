package authoritycoord

import (
	"strings"
	"sync"
)

// settlementTracker is shared across CompensationStack value copies and
// coordinates per-provider settlement so concurrent Settle calls invoke each
// provider at most once (requirements 7.7, 8.6).
type settlementTracker struct {
	mu        sync.Mutex
	providers map[string]*providerSettleRecord
}

type providerSettleRecord struct {
	done     bool
	inflight bool
	lastErr  error
	waiters  []chan struct{}
}

func newSettlementTracker() *settlementTracker {
	return &settlementTracker{providers: make(map[string]*providerSettleRecord)}
}

// beginSettle claims settlement work for providerID.
// skip=true means already successful (do not invoke).
// wait!=nil means another caller is in-flight; wait then use waitResult.
// finish!=nil means this caller owns the invoke and must call finish(err).
func (t *settlementTracker) beginSettle(providerID string) (skip bool, wait <-chan struct{}, finish func(error)) {
	if t == nil {
		return false, nil, func(error) {}
	}
	id := strings.TrimSpace(providerID)
	if id == "" {
		return true, nil, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.providers[id]
	if st == nil {
		st = &providerSettleRecord{}
		t.providers[id] = st
	}
	if st.done {
		return true, nil, nil
	}
	if st.inflight {
		ch := make(chan struct{})
		st.waiters = append(st.waiters, ch)
		return false, ch, nil
	}
	st.inflight = true
	st.lastErr = nil
	return false, nil, func(err error) {
		t.finishSettle(id, err)
	}
}

func (t *settlementTracker) finishSettle(providerID string, err error) {
	if t == nil {
		return
	}
	id := strings.TrimSpace(providerID)
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.providers[id]
	if st == nil {
		return
	}
	st.inflight = false
	if err == nil {
		st.done = true
		st.lastErr = nil
	} else {
		st.done = false
		st.lastErr = err
	}
	for _, w := range st.waiters {
		close(w)
	}
	st.waiters = nil
}

// waitResult returns nil when settlement succeeded; otherwise the in-flight
// failure (provider remains retryable).
func (t *settlementTracker) waitResult(providerID string) error {
	if t == nil {
		return nil
	}
	id := strings.TrimSpace(providerID)
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.providers[id]
	if st == nil || st.done {
		return nil
	}
	return st.lastErr
}

func (t *settlementTracker) waitingCount(providerID string) int {
	if t == nil {
		return 0
	}
	id := strings.TrimSpace(providerID)
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.providers[id]
	if st == nil {
		return 0
	}
	return len(st.waiters)
}

// SettleWaitingCount reports in-flight waiters for providerID (test/observability).
func (s CompensationStack) SettleWaitingCount(providerID string) int {
	if s.settle == nil {
		return 0
	}
	return s.settle.waitingCount(providerID)
}
