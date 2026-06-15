package runtime

import (
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
)

type attemptBudget struct {
	mu   sync.Mutex
	max  int
	used int
}

func (b *attemptBudget) tryAcquire() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.max {
		return false
	}
	b.used++
	return true
}

func (e *Executor) effectiveMaxAttempts() int {
	if e == nil || e.MaxAttempts <= 0 {
		return 3
	}
	return e.MaxAttempts
}

type lockedRng struct {
	mu   sync.Mutex
	base routing.Rng
}

func (l *lockedRng) Intn(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.base.Intn(n)
}

var _ routing.Rng = (*lockedRng)(nil)

func (e *Executor) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) WallClock() func() time.Time {
	if e == nil {
		return nil
	}
	return e.Now
}

func (e *Executor) rng() routing.Rng {
	if e.Rand != nil {
		e.rngOnce.Do(func() {
			e.lockedRand = &lockedRng{base: e.Rand}
		})
		return e.lockedRand
	}
	return routing.NewSeededRng(1)
}

func (e *Executor) mergePlannerHealth() map[string]struct{} {
	if e == nil || e.CandidateHealth == nil {
		return nil
	}
	return e.CandidateHealth.UnhealthyCandidateKeys()
}
