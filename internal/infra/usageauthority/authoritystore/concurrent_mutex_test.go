package authoritystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	_ "modernc.org/sqlite"
)

// TestDurableStore_UnrelatedReservesDoNotHoldProcessMutexAcrossDB proves Phase 12.1:
// mutation paths must not hold a process-wide mutex across BeginTx. Under the old
// DurableStore.mu wrapping Reserve end-to-end, N goroutines cannot all reach the
// BeginTx barrier while one holds the mutex.
func TestDurableStore_UnrelatedReservesDoNotHoldProcessMutexAcrossDB(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}

	const n = 4
	rows := make([]controlplane.AccountingLimitStatusRow, 0, n)
	for i := range n {
		rows = append(rows, controlplane.AccountingLimitStatusRow{
			RuleID:         fmtRuleID(i),
			RuleType:       string(domain.RuleKindQuota),
			Unit:           string(domain.AmountUnitRequests),
			Limit:          100,
			Remaining:      100,
			Authority:      controlplane.AccountingAuthoritySourceAuthoritative,
			EvidenceState:  controlplane.EvidenceRecorded,
			RedactionState: controlplane.RedactionSummarized,
		})
	}
	store, err := NewDurable(ctx, bunDB, Config{
		StoreID:   "concurrent-unrelated",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: rows,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var entered, release sync.WaitGroup
	entered.Add(n)
	release.Add(1)
	store.beginTxHook = func() {
		entered.Done()
		release.Wait()
	}

	errs := make(chan error, n)
	for i := range n {
		go func() {
			cmd := reconcileReserveCommandInternal(fmtRuleID(i), 1)
			cmd.ReservationKey.LogicalRequestID = fmtLogical(i)
			cmd.ReservationKey.AttemptID = fmtAttempt(i)
			cmd.SourceKey = cmd.ReservationKey.String()
			_, err := store.Reserve(ctx, cmd)
			errs <- err
		}()
	}

	done := make(chan struct{})
	go func() {
		entered.Wait()
		close(done)
	}()
	select {
	case <-done:
		// All mutation paths reached BeginTx concurrently — process mutex is not held across DB work.
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for concurrent Reserve paths to reach BeginTx; process-wide mutex likely serializes DB work (16.1/16.2)")
	}
	release.Done()

	for i := range n {
		if err := <-errs; err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
}

func fmtRuleID(i int) string {
	return "rule-" + string(rune('a'+i))
}

func fmtLogical(i int) string {
	return "req-" + string(rune('a'+i))
}

func fmtAttempt(i int) string {
	return "attempt-" + string(rune('a'+i))
}
