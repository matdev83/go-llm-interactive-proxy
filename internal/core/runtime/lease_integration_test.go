package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type recordingConcurrency struct {
	admitGen    int64
	released    atomic.Int32
	renewed     atomic.Int32
	renewFail   atomic.Bool
	renewCh     chan struct{}
	releaseCh   chan struct{}
	expiresIn   time.Duration
	renewBefore time.Duration
}

func (r *recordingConcurrency) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	ttl := r.expiresIn
	if ttl <= 0 {
		ttl = 200 * time.Millisecond
	}
	renewBefore := r.renewBefore
	if renewBefore <= 0 {
		renewBefore = 50 * time.Millisecond
	}
	return authority.LeaseDecision{
		Kind:        authority.LeaseAllow,
		LeaseID:     "lease-hb",
		Generation:  1,
		ExpiresAt:   time.Now().Add(ttl),
		RenewBefore: renewBefore,
		TTL:         ttl,
	}, nil
}

func (r *recordingConcurrency) RenewLease(ctx context.Context, in authority.LeaseRenew) (authority.LeaseDecision, error) {
	if r.renewFail.Load() {
		return authority.LeaseDecision{}, context.DeadlineExceeded
	}
	r.renewed.Add(1)
	select {
	case r.renewCh <- struct{}{}:
	default:
	}
	ttl := r.expiresIn
	if ttl <= 0 {
		ttl = 200 * time.Millisecond
	}
	return authority.LeaseDecision{
		Kind:       authority.LeaseAllow,
		LeaseID:    in.LeaseID,
		Generation: in.ExpectedGeneration + 1,
		ExpiresAt:  time.Now().Add(ttl),
	}, nil
}

func (r *recordingConcurrency) ReleaseLease(context.Context, authority.LeaseRelease) error {
	r.released.Add(1)
	select {
	case r.releaseCh <- struct{}{}:
	default:
	}
	return nil
}

func (r *recordingConcurrency) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestPhase83_settleReleasesConcurrencyLease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{releaseCh: make(chan struct{}, 1)}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-settle", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.LeaseID != "lease-hb" {
		t.Fatalf("state=%+v", st)
	}
	ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d want 1 (settle must ReleaseLease; Settle alone does not)", conc.released.Load())
	}
	ex.settleRequestAuthority(ctx, nil) // idempotent
	if conc.released.Load() != 1 {
		t.Fatalf("idempotent settle released=%d", conc.released.Load())
	}
}

func TestPhase83_releaseStopsHeartbeatAndReleasesLease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:   5 * time.Second,
		renewBefore: 4 * time.Second,
		renewCh:     make(chan struct{}, 4),
		releaseCh:   make(chan struct{}, 1),
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-rel", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	ex.releaseRequestAuthority(ctx)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d want 1", conc.released.Load())
	}
	time.Sleep(200 * time.Millisecond)
	if conc.renewed.Load() != 0 {
		t.Fatalf("heartbeat must stop on release; renewed=%d", conc.renewed.Load())
	}
}

func TestPhase83_heartbeatRenewsBeforeExpiry(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:   150 * time.Millisecond,
		renewBefore: 100 * time.Millisecond,
		renewCh:     make(chan struct{}, 2),
		releaseCh:   make(chan struct{}, 1),
	}
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-hb", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-conc.renewCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected lease renew before expiry")
	}
	ex.settleRequestAuthority(ctx, nil)
	if conc.released.Load() != 1 {
		t.Fatalf("released=%d", conc.released.Load())
	}
}

func TestPhase83_renewFailureMarksDegradedWithoutRelease(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{
		expiresIn:   120 * time.Millisecond,
		renewBefore: 90 * time.Millisecond,
		renewCh:     make(chan struct{}, 1),
		releaseCh:   make(chan struct{}, 1),
	}
	conc.renewFail.Store(true)
	ex := &Executor{
		AccountingRuntime: AccountingRuntime{
			RequestCoordinator: &authoritycoord.RequestCoordinator{
				Concurrency:    conc,
				CleanupTimeout: time.Second,
			},
		},
	}
	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-deg", "a1", "t1", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatal(err)
	}
	st := requestAuthorityFrom(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st.heartbeat != nil && st.heartbeat.Degraded() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.heartbeat == nil || !st.heartbeat.Degraded() {
		t.Fatal("expected degraded heartbeat after renew failure")
	}
	if conc.released.Load() != 0 {
		t.Fatalf("renew failure must not release lease; released=%d", conc.released.Load())
	}
	ex.releaseRequestAuthority(ctx)
}

func TestBuildAuthorityCoordinators_WiresConcurrency(t *testing.T) {
	t.Parallel()
	conc := &recordingConcurrency{}
	req, att := BuildAuthorityCoordinators(nil, conc)
	if req == nil || req.Concurrency != conc {
		t.Fatalf("req=%v", req)
	}
	if att != nil {
		t.Fatal("attempt coordinator should be nil without usage authority")
	}
}
