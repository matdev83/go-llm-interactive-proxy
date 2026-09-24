package billingstore

// Phase 17.3 Finding F1 RED: activation must serialize with monetary
// transactions via a shared per-store marker lock.
//
// Defect under test (must FAIL before F1, PASS after):
// PostgreSQL ordinary SELECT does not lock the marker. Activation drain
// check occurs outside the transition tx. A V1 adjustment can read shadow
// marker, pause with uncommitted pin, activation sees no pin and commits
// v2_active, then V1 posts after activation.
//
// Barrier (no sleeps for ordering; timeout only bounds liveness while the
// V1 tx holds its uncommitted pin):
//  1. Shadow + funded account, start V1 direct adjustment, pause at
//     b2b4-before-effects (after marker check + uncommitted pin insert).
//  2. Concurrent BeginDraining+Activate while V1 paused must block/wait
//     (fixed) rather than commit v2_active on stale reads (bug).
//  3. Release V1; fixed order commits V1 before activation (safe), buggy
//     order commits activation before V1 (V1 money after v2_active).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

func f1SetupShadowFundedAccount(t *testing.T, store *DurableStore, accountID string) context.Context {
	t.Helper()
	ctx := context.Background()
	acct := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == billing.AccountingCutoverV1Active {
		if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
			ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
			NextState:    billing.AccountingCutoverV2Shadow,
			TransitionID: "f1-shadow",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

func TestF1SQLiteActivationSerializedWithDirectAdjustment(t *testing.T) {
	t.Parallel()
	// File WAL database: readers do not block writers, so the PG defect
	// (ordinary SELECT without FOR UPDATE + drain outside tx) reproduces on
	// SQLite. Memory rollback-journal databases serialize via the single
	// writer and would mask the bug.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", filepath.ToSlash(filepath.Join(t.TempDir(), "f1-sqlite-wal.db")))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	seedTestSchemaIfEmpty(t, bunDB)
	store, err := NewDurableStore(context.Background(), bunDB, Config{StoreID: "f1-sqlite-direct"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountID := "acct-f1-sqlite-direct"
	ctx := f1SetupShadowFundedAccount(t, store, accountID)

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

	adj := billing.AdjustmentInput{AccountID: accountID, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "f1-direct-1", Reason: "correction"}
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
	case <-time.After(10 * time.Second):
		t.Fatalf("F1: V1 adjustment never reached b2b4-pin-acquire (ordering barrier failed)")
	}

	type drainRes struct {
		marker billing.AccountingCutoverMarker
		err    error
	}
	drainDone := make(chan drainRes, 1)
	go func() {
		// Draining + activation while V1 holds uncommitted pin must block.
		_, _, derr := store.BeginCutoverDraining(ctx, "f1-drain")
		if derr != nil {
			drainDone <- drainRes{err: derr}
			return
		}
		m, aerr := store.ActivateCutoverV2(ctx, "f1-activate")
		drainDone <- drainRes{marker: m, err: aerr}
	}()

	// While V1 paused with uncommitted pin, activation must NOT complete.
	// Timeout only bounds liveness; ordering is via entered/release barriers.
	select {
	case r := <-drainDone:
		close(release)
		// Drain/activate completed while V1 uncommitted pin invisible: BUG.
		// Drain the posting result to avoid goroutine leak, then fail.
		pr := <-postDone
		t.Fatalf("F1 RED: activation completed while V1 in-flight (drain err=%v, V1 err=%v); V1 money committed after v2_active without serialization", r.err, pr.err)
	case <-time.After(2 * time.Second):
		// Still blocked while V1 paused: FIXED (waits on shared marker).
	}

	close(release)
	pr := <-postDone
	if pr.err != nil {
		t.Fatalf("F1: V1 adjustment after release: %v", pr.err)
	}
	dr := <-drainDone
	if dr.err != nil {
		t.Fatalf("F1: draining+activation after V1 commit: %v", dr.err)
	}
	if dr.marker.State != billing.AccountingCutoverV2Active {
		t.Fatalf("F1: final marker = %q, want v2_active", dr.marker.State)
	}
	// V1 committed before activation (safe order): exactly one V1 journal,
	// no V1 money after v2_active. Buggy order would have committed
	// activation first then V1 (detected above as early drainDone).
	if n := b2b4DirectJournals(t, store, accountID); n != 1 {
		t.Fatalf("F1: direct journals = %d, want 1 (V1 before activation)", n)
	}
	_ = errors.Is
}
