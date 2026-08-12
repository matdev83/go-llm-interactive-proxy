package billingstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	dbinfra "github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
)

func TestSQLiteConcurrentAuthorizationsNeverOverspend(t *testing.T) {
	dsn := fmt.Sprintf("file:billing-authorization-concurrency-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", testSequence.Add(1))
	sqlDB1, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB1.SetMaxOpenConns(8)
	bunDB1, err := dbinfra.NewBunDB(sqlDB1, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB1.Close()
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB1, Config{StoreID: "first-process"})
	if err != nil {
		_ = bunDB1.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB2.SetMaxOpenConns(8)
	bunDB2, err := dbinfra.NewBunDB(sqlDB2, dbinfra.DialectSQLite)
	if err != nil {
		_ = sqlDB2.Close()
		t.Fatal(err)
	}
	second, err := OpenStore(context.Background(), bunDB2, Config{StoreID: "second-process"})
	if err != nil {
		_ = bunDB2.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB2.Close() })

	runConcurrentAuthorizationContract(t, store, second, "concurrent-auth")
}

func runConcurrentAuthorizationContract(t *testing.T, first, second *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := first.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	const workers = 80
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	errorsByKind := make(map[string]int)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := first
			if i%2 == 1 {
				target = second
			}
			_, callErr := target.Authorize(ctx, authorizationInput(accountID, "turn-"+authItoa(i), "auth-"+authItoa(i), 3))
			mu.Lock()
			defer mu.Unlock()
			if callErr == nil {
				accepted++
			} else {
				errorsByKind[callErr.Error()]++
			}
		}(i)
	}
	wg.Wait()
	account, err := first.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted > 33 || account.ReservedNano > 100 || account.ReservedNano != int64(accepted*3) {
		t.Fatalf("accepted=%d account=%+v errors=%v", accepted, account, errorsByKind)
	}
	journals, err := first.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != accepted {
		t.Fatalf("journal count=%d accepted=%d", len(journals), accepted)
	}
	for i, journal := range journals {
		if journal.AccountSequence != uint64(i+1) {
			t.Fatalf("journal sequence[%d]=%d want %d", i, journal.AccountSequence, i+1)
		}
	}
}

func authItoa(v int) string {
	// The test only needs deterministic compact IDs; avoid pulling a formatter
	// into the store implementation.
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}
