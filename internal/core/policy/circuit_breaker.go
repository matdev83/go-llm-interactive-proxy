package policy

import (
	"sync"
	"time"
)

// ThresholdCircuit marks routing keys unhealthy after consecutive failures within a TTL window.
// It is a minimal circuit abstraction feeding CandidateHealth (not a full distributed breaker).
type ThresholdCircuit struct {
	mu         sync.Mutex
	threshold  int
	window     time.Duration
	failCounts map[string]int
	lastFail   map[string]time.Time
	now        func() time.Time
}

// NewThresholdCircuit returns a circuit helper; threshold must be >= 1, window must be > 0.
func NewThresholdCircuit(threshold int, window time.Duration) *ThresholdCircuit {
	if threshold < 1 {
		threshold = 3
	}
	if window <= 0 {
		window = 30 * time.Second
	}
	return &ThresholdCircuit{
		threshold:  threshold,
		window:     window,
		failCounts: map[string]int{},
		lastFail:   map[string]time.Time{},
		now:        time.Now,
	}
}

// RecordFailure increments failure count for a candidate key and opens when threshold reached.
func (c *ThresholdCircuit) RecordFailure(key string) {
	if c == nil || key == "" {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failCounts[key]++
	c.lastFail[key] = now
}

// RecordSuccess clears failure state for a key.
func (c *ThresholdCircuit) RecordSuccess(key string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failCounts, key)
	delete(c.lastFail, key)
}

// UnhealthyCandidateKeys returns keys currently considered open (failures >= threshold within window).
func (c *ThresholdCircuit) UnhealthyCandidateKeys() map[string]struct{} {
	if c == nil {
		return nil
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]struct{})
	for k, n := range c.failCounts {
		if n < c.threshold {
			continue
		}
		if t, ok := c.lastFail[k]; ok && now.Sub(t) <= c.window {
			out[k] = struct{}{}
		}
	}
	return out
}
