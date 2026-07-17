package runtime

import (
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const canonicalUnrepresentableReplay = "unrepresentable_replay"

type transformExcludeTracker struct {
	mu              sync.Mutex
	count           int
	unrepresentable int
	nonTransform    bool
}

func (t *transformExcludeTracker) noteTransform(reason string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	if reason == canonicalUnrepresentableReplay {
		t.unrepresentable++
	}
}

func (t *transformExcludeTracker) noteOther() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nonTransform = true
}

func (t *transformExcludeTracker) allExcludedError() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count == 0 || t.nonTransform {
		return nil
	}
	if t.unrepresentable == t.count {
		return lipapi.ErrAllCandidatesUnrepresentableReplay
	}
	return lipapi.ErrAllCandidatesExcluded
}
