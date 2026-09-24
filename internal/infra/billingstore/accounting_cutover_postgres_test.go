//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3A PostgreSQL parity: same durable cutover marker contract on
// configured direct PostgreSQL. Skips unless LIP_REQUIRE_POSTGRES=1 and a DSN
// is configured; the SQLite suite in accounting_cutover_test.go is
// authoritative when PostgreSQL is unavailable.
func TestAccountingCutoverPostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "cutover-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	current, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("Ensure postgres: %v", err)
	}
	if current.State != billing.AccountingCutoverV1Active {
		t.Fatalf("postgres default = %q, want v1_active", current.State)
	}
	if current.ActivePostingOwner != billing.AccountingPostingOwnerV1 {
		t.Fatalf("postgres default owner = %q, want v1", current.ActivePostingOwner)
	}
	req := billing.AccountingCutoverTransition{ExpectedVersion: current.Version, ExpectedEpoch: current.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "pg-shadow"}
	advanced, err := store.TransitionAccountingCutover(ctx, req)
	if err != nil {
		t.Fatalf("Transition postgres: %v", err)
	}
	replayed, err := store.TransitionAccountingCutover(ctx, req)
	if err != nil {
		t.Fatalf("replay postgres: %v", err)
	}
	if replayed.Version != advanced.Version || replayed.TransitionID != advanced.TransitionID {
		t.Fatalf("postgres replay mismatch")
	}
	// Store isolation on the same PostgreSQL schema boundary.
	other, err := NewDurableStore(ctx, store.DB(), Config{StoreID: "cutover-pg-other"})
	if err != nil {
		t.Fatalf("NewDurableStore other postgres: %v", err)
	}
	otherMarker, err := other.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatalf("Ensure other postgres: %v", err)
	}
	if otherMarker.State != billing.AccountingCutoverV1Active || otherMarker.Version != 1 {
		t.Fatalf("postgres isolation violated: %#v", otherMarker)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres after cutover: %v", err)
	}
}
