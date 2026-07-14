package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
)

// leaseHeartbeat owns one request-scoped renew loop. Stop is idempotent.
type leaseHeartbeat struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	degraded atomic.Bool
}

func newLeaseHeartbeat() *leaseHeartbeat {
	return &leaseHeartbeat{
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (h *leaseHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stopCh) })
	<-h.doneCh
}

func (h *leaseHeartbeat) Degraded() bool {
	return h != nil && h.degraded.Load()
}

func (e *Executor) startLeaseHeartbeat(parent context.Context, st *requestAuthorityState) {
	if e == nil || st == nil || e.RequestCoordinator == nil || e.RequestCoordinator.Concurrency == nil {
		return
	}
	leaseID := st.LeaseID
	if leaseID == "" {
		leaseID = st.Decision.Lease.LeaseID
	}
	if leaseID == "" {
		return
	}
	gen := st.LeaseGeneration
	if gen == 0 {
		gen = st.Decision.Lease.Generation
	}
	expires := st.LeaseExpiresAt
	if expires.IsZero() {
		expires = st.Decision.Lease.ExpiresAt
	}
	renewBefore := st.RenewBefore
	if renewBefore <= 0 {
		renewBefore = st.Decision.Lease.RenewBefore
	}
	if renewBefore <= 0 {
		renewBefore = e.ConcurrencyRenewBefore
	}
	if renewBefore <= 0 {
		renewBefore = 15 * time.Second
	}
	ttl := st.LeaseTTL
	if ttl <= 0 {
		ttl = st.Decision.Lease.TTL
	}
	if ttl <= 0 {
		ttl = e.ConcurrencyLeaseTTL
	}
	if ttl <= 0 {
		ttl = time.Minute
	}

	st.LeaseID = leaseID
	st.LeaseGeneration = gen
	st.LeaseExpiresAt = expires
	st.RenewBefore = renewBefore
	st.LeaseTTL = ttl

	hb := newLeaseHeartbeat()
	st.heartbeat = hb
	prov := e.RequestCoordinator.Concurrency
	reqID := st.RequestID
	cleanupTimeout := e.RequestCoordinator.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = 2 * time.Second
	}

	go func() {
		defer close(hb.doneCh)
		generation := gen
		expiresAt := expires
		for {
			wait := time.Until(expiresAt.Add(-renewBefore))
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-hb.stopCh:
				timer.Stop()
				return
			case <-parent.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-hb.stopCh:
				return
			default:
			}
			rctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanupTimeout)
			dec, err := prov.RenewLease(rctx, authority.LeaseRenew{
				LeaseID:            leaseID,
				RequestID:          reqID,
				ExpectedGeneration: generation,
				TTL:                ttl,
			})
			cancel()
			if err != nil {
				// Renewal storage failure must not corrupt active count (10.8):
				// leave the lease live until expiry/terminal release.
				hb.degraded.Store(true)
				behavior := st.FailureBehavior
				if behavior == "" {
					behavior = st.Decision.Lease.FailureBehavior
				}
				if behavior == "" {
					behavior = authority.FailureFailClosed
				}
				if behavior == authority.FailureFailClosed {
					// Fail-closed: stop renewing; occupancy remains until expiry
					// or terminal release without mutating the active count.
					return
				}
				// Fail-open: keep retrying to hold occupancy through transient failures.
				retryWait := time.Until(expiresAt) / 2
				if retryWait < 100*time.Millisecond {
					retryWait = 100 * time.Millisecond
				}
				if retryWait > renewBefore {
					retryWait = renewBefore
				}
				retry := time.NewTimer(retryWait)
				select {
				case <-hb.stopCh:
					retry.Stop()
					return
				case <-parent.Done():
					retry.Stop()
					return
				case <-retry.C:
				}
				continue
			}
			if dec.Generation > 0 {
				generation = dec.Generation
				st.LeaseGeneration = dec.Generation
			}
			if !dec.ExpiresAt.IsZero() {
				expiresAt = dec.ExpiresAt
				st.LeaseExpiresAt = dec.ExpiresAt
			}
		}
	}()
}

func (e *Executor) stopLeaseHeartbeat(st *requestAuthorityState) {
	if st == nil || st.heartbeat == nil {
		return
	}
	st.heartbeat.Stop()
	st.heartbeat = nil
}
