package authoritystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

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
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
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

	var entered, release, finished sync.WaitGroup
	entered.Add(n)
	release.Add(1)
	store.beginTxHook = func() {
		entered.Done()
		release.Wait()
	}

	errs := make(chan error, n)
	for i := range n {
		finished.Add(1)
		go func() {
			defer finished.Done()
			cmd := reconcileReserveCommandInternal(fmtRuleID(i), 1)
			cmd.ReservationKey.LogicalRequestID = fmtLogical(i)
			cmd.ReservationKey.AttemptID = fmtAttempt(i)
			cmd.SourceKey = cmd.ReservationKey.String()
			_, err := store.Reserve(ctx, cmd)
			errs <- err
		}()
	}

	// All mutation paths reach BeginTx concurrently — proves process-wide mutex does not serialize DB work.
	entered.Wait()
	release.Done()
	finished.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("reserve error: %v", err)
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
