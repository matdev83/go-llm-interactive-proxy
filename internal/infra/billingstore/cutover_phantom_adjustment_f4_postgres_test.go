//go:build integration

package billingstore

// Phase 17.3 F4 PostgreSQL proof: ordinary completed provider history with no
// pending adjustment leaves no phantom financial_adjustment pin and activates.

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestF4OrdinaryHistoryActivatesOnPostgresWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f4-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-f4-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f4-pg-shadow"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		callID, err := billing.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ApplyProviderCostRevision(ctx, f4ProviderRevisionInput(store.StoreID(), acct.ID, callID, fmt.Sprintf("f4-pg-head-%d", i), 1, 10)); err != nil {
			t.Fatalf("seed pg %d: %v", i, err)
		}
	}
	if _, _, err := store.BeginCutoverDraining(ctx, "f4-pg-drain"); err != nil {
		t.Fatalf("BeginCutoverDraining pg: %v", err)
	}
	var pinned int
	if err := store.DB().NewRaw(`SELECT COUNT(1) FROM billing_posting_ownership_pins WHERE store_id = ? AND operation_kind = ? AND status = ?`,
		store.StoreID(), string(billing.PostingOperationFinancialAdjustment), string(billing.PostingPinPinned)).Scan(ctx, &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned != 0 {
		t.Fatalf("F4 phantom pg: pinned = %d, want 0", pinned)
	}
	status, err := store.CutoverDrainStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ReadyForActivation {
		t.Fatalf("pg drain must be ready: %+v", status.Counts)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f4-pg-activate"); err != nil {
		t.Fatalf("pg activate after ordinary history: %v", err)
	}
}
