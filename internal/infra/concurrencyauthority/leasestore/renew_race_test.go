package leasestore_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/domain"
)

// TestSQLiteStore_ConcurrentReleaseRenew_NoResurrection races Renew against Release.
// After a successful Release, the lease must not end active.
func TestSQLiteStore_ConcurrentReleaseRenew_NoResurrection(t *testing.T) {
	t.Parallel()
	store := newSQLiteStore(t, filepath.Join(t.TempDir(), "cas-race.db"), "sqlite-cas-race")
	runConcurrentReleaseRenewNoResurrection(t, store)
}

func runConcurrentReleaseRenewNoResurrection(t *testing.T, store app.LeaseStore) {
	t.Helper()
	ctx := context.Background()
	const rounds = 40
	for i := 0; i < rounds; i++ {
		now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		leaseID := "lease-race"
		acq, err := store.Acquire(ctx, acquireCmd(leaseID, "req-race", now, time.Minute))
		if err != nil {
			t.Fatalf("round %d acquire: %v", i, err)
		}
		gen := acq.Lease.Generation

		var (
			wg       sync.WaitGroup
			renewErr error
			relErr   error
			released bool
		)
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			renewErr = retrySQLiteBusy(func() error {
				_, err := store.Renew(ctx, app.RenewCommand{
					LeaseID:            leaseID,
					ExpectedGeneration: gen,
					TTL:                time.Minute,
					Now:                now.Add(time.Second),
				})
				return err
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			relErr = retrySQLiteBusy(func() error {
				rel, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now.Add(time.Second)})
				if err != nil {
					return err
				}
				released = rel.Applied
				return nil
			})
		}()
		close(start)
		wg.Wait()

		if relErr != nil {
			t.Fatalf("round %d release: %v", i, relErr)
		}
		if renewErr != nil && !domain.IsCASError(renewErr) && !errors.Is(renewErr, app.ErrNotFound) {
			t.Fatalf("round %d renew unexpected err=%v", i, renewErr)
		}

		q, err := store.Query(ctx, app.QueryCommand{LeaseID: leaseID, Now: now.Add(2 * time.Second), Limit: 1})
		if err != nil {
			t.Fatalf("round %d query: %v", i, err)
		}
		if len(q.Leases) != 1 {
			t.Fatalf("round %d expected 1 lease, got %d", i, len(q.Leases))
		}
		final := q.Leases[0]
		if released && final.State == domain.LeaseStateActive {
			t.Fatalf("round %d: release applied but lease resurrected to active (renewErr=%v)", i, renewErr)
		}
		// Clean slate for next round: ensure released so Acquire can reuse lease_id.
		if final.State != domain.LeaseStateReleased {
			if _, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now.Add(3 * time.Second)}); err != nil {
				t.Fatalf("round %d cleanup release: %v", i, err)
			}
		}
	}
}

func retrySQLiteBusy(fn func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = fn()
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}
