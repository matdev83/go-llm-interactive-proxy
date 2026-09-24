//go:build integration

package billingstore

// Phase 17.3 Finding F1 PostgreSQL barrier: same serialization contract on
// configured direct PostgreSQL using two pooled connections. Skips unless
// LIP_REQUIRE_POSTGRES=1 and a DSN is configured; the SQLite suite is
// authoritative when PostgreSQL is unavailable.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestF1PostgresActivationSerializedWithDirectAdjustment(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f1-pg-direct"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountID := "acct-f1-pg-direct"
	f1SetupShadowFundedAccount(t, store, accountID)

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	store.SetAdjustmentFaultHook(func(point string) error {
		if point == "b2b4-pin-acquire" {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	defer store.SetAdjustmentFaultHook(nil)

	adj := billing.AdjustmentInput{AccountID: accountID, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f1-pg-direct-1", Reason: "correction"}
	type postRes struct {
		posting billing.Posting
		err     error
	}
	postDone := make(chan postRes, 1)
	go func() {
		p, err := store.PostAdjustment(ctx, adj)
		postDone <- postRes{posting: p, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatalf("F1 PG: V1 adjustment never reached b2b4-pin-acquire")
	}

	type drainRes struct {
		marker billing.AccountingCutoverMarker
		err    error
	}
	drainDone := make(chan drainRes, 1)
	go func() {
		_, _, derr := store.BeginCutoverDraining(ctx, "f1-pg-drain")
		if derr != nil {
			drainDone <- drainRes{err: derr}
			return
		}
		m, aerr := store.ActivateCutoverV2(ctx, "f1-pg-activate")
		drainDone <- drainRes{marker: m, err: aerr}
	}()

	select {
	case r := <-drainDone:
		close(release)
		pr := <-postDone
		t.Fatalf("F1 PG RED: activation completed while V1 in-flight (drain err=%v, V1 err=%v); missing SELECT FOR UPDATE serialization", r.err, pr.err)
	case <-time.After(5 * time.Second):
		// Blocked on shared per-store marker row: FIXED.
	}

	close(release)
	pr := <-postDone
	if pr.err != nil {
		t.Fatalf("F1 PG: V1 adjustment after release: %v", pr.err)
	}
	dr := <-drainDone
	if dr.err != nil {
		t.Fatalf("F1 PG: draining+activation after V1 commit: %v", dr.err)
	}
	if dr.marker.State != billing.AccountingCutoverV2Active {
		t.Fatalf("F1 PG: final marker = %q, want v2_active", dr.marker.State)
	}
	if n := b2b4DirectJournals(t, store, accountID); n != 1 {
		t.Fatalf("F1 PG: direct journals = %d, want 1", n)
	}
}
