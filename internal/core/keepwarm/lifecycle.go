package keepwarm

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func (m *Manager) releaseObservationsLocked(observations []promptcache.Observation, controller promptcache.Controller) {
	for _, o := range observations {
		m.enqueueReleaseLocked(controller, o.Handle)
	}
}

func (m *Manager) enqueueReleaseLocked(controller promptcache.Controller, handle promptcache.Handle) {
	if controller == nil || len(handle) == 0 {
		return
	}
	job := releaseJob{controller: controller, handle: append(promptcache.Handle(nil), handle...)}
	if m.releaseClosed {
		// Late observations still release their local handles asynchronously
		// (pinned by TestManagerQuiesceRejectsLateArmAndWaitsForRelease). The
		// RPC is bounded by renew_timeout and the backend target store is itself
		// bounded, so an escaping best-effort release is harmless.
		go m.releaseOne(job)
		return
	}
	if !m.releaseStarted {
		m.releaseStarted = true
		m.releaseWG.Add(1)
		go m.releaseLoop()
	}
	m.releaseQueue = append(m.releaseQueue, job)
	select {
	case m.releaseWake <- struct{}{}:
	default:
	}
}

func (m *Manager) releaseLoop() {
	defer m.releaseWG.Done()
	for {
		m.mu.Lock()
		if len(m.releaseQueue) > 0 {
			job := m.releaseQueue[0]
			m.releaseQueue[0] = releaseJob{}
			m.releaseQueue = m.releaseQueue[1:]
			m.mu.Unlock()
			m.releaseOne(job)
			continue
		}
		if m.releaseClosed {
			m.mu.Unlock()
			return
		}
		wake := m.releaseWake
		m.mu.Unlock()
		<-wake
	}
}

func (m *Manager) releaseOne(job releaseJob) {
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.RenewTimeout)
	defer cancel()
	_ = job.controller.Release(ctx, promptcache.ReleaseRequest{Handle: job.handle})
}

func (m *Manager) metricLocked(name string) {
	m.metrics[name]++
	if m.hooks.Metric != nil {
		m.hooks.Metric(name)
	}
}

type MetricsSnapshot struct {
	ActiveEpochs  int
	ActiveTargets int
	Events        map[string]uint64
}

func (m *Manager) Metrics() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := make(map[string]uint64, len(m.metrics))
	for name, count := range m.metrics {
		events[name] = count
	}
	targets := 0
	for _, epoch := range m.epochs {
		targets += len(epoch.targets)
	}
	return MetricsSnapshot{ActiveEpochs: len(m.epochs), ActiveTargets: targets, Events: events}
}

func (m *Manager) Quiesce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.quiescing {
		m.mu.Unlock()
		return nil
	}
	m.quiescing = true
	for a := range m.epochs {
		m.invalidateLocked(a, "quiesce")
	}
	if m.runCancel != nil {
		m.runCancel()
	}
	m.mu.Unlock()
	workersDone := make(chan struct{})
	go func() { m.runWG.Wait(); m.renewWG.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.mu.Lock()
	if !m.releaseClosed {
		m.releaseClosed = true
		select {
		case m.releaseWake <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
	releasesDone := make(chan struct{})
	go func() { m.releaseWG.Wait(); close(releasesDone) }()
	select {
	case <-releasesDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) ActiveTargetCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.epochs {
		n += len(e.targets)
	}
	return n
}

func (m *Manager) ActiveEpochCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.epochs)
}

func (m *Manager) NextDue() (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out time.Time
	ok := false
	for _, e := range m.epochs {
		for _, t := range e.targets {
			if !ok || t.dueAt.Before(out) {
				out = t.dueAt
				ok = true
			}
		}
	}
	return out, ok
}
