package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/db"
	_ "modernc.org/sqlite"
)

// Task 17.4: startup wiring consults the durable recovery snapshot port.
// A nil store carries no durable state and keeps prior behavior; every
// non-nil internal store must expose the snapshot port (a decorator hiding
// it fails closed). Readable snapshot-capable stores pass with the current
// capability; unreadable, malformed, or incompatible stores fail closed
// before any worker starts.

// wiringNoSnapshotStore hides the recovery snapshot port behind interface
// embedding while still exposing the monetary/reporting ports of the
// wrapped store. It models the F1 decorator that must be rejected.
type wiringNoSnapshotStore struct {
	billing.AuthoritativeBilling
	billing.CallUsageStore
}

var recoveryWiringTestSequence atomic.Int64

func newRecoveryWiringStore(t *testing.T, storeID string) *billingstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:recovery-wiring-174-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)", recoveryWiringTestSequence.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := billingstore.NewDurableStore(context.Background(), bunDB, billingstore.Config{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestVerifyBillingAccountingStartupWiring(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := verifyBillingAccountingStartup(ctx, nil); err != nil {
		t.Fatalf("nil store must keep prior behavior: %v", err)
	}
	inner := processBillingStore{}
	if err := verifyBillingAccountingStartup(ctx, processBillingStore{}); err != nil {
		t.Fatalf("explicit safe-snapshot double must pass current capability: %v", err)
	}
	hidden := wiringNoSnapshotStore{AuthoritativeBilling: inner, CallUsageStore: inner}
	if err := verifyBillingAccountingStartup(ctx, hidden); err == nil {
		t.Fatalf("store hiding the recovery snapshot port must fail closed")
	}
	fresh := newRecoveryWiringStore(t, "rec174-wire-fresh")
	if err := verifyBillingAccountingStartup(ctx, fresh); err != nil {
		t.Fatalf("fresh snapshot-capable store must pass current capability: %v", err)
	}
	broken := newRecoveryWiringStore(t, "rec174-wire-broken")
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyBillingAccountingStartup(ctx, broken); err == nil {
		t.Fatalf("unreadable store must fail closed before workers start")
	}
}
