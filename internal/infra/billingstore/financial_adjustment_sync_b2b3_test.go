package billingstore

// Phase 17.3 B2b3 synchronous marker gates: nonqueued direct/customer
// adjustments validate the current marker at execution; exact replay stays
// stable. Queued provider/selected-cost commands carry claim metadata (B2b1/
// B2b2/B2b3 pin paths); these sync commands have no queue and no head pins.

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestB2b3SyncAdjustmentMarkerGates(t *testing.T) {
	t.Parallel()
	store := b2b3NewStore(t, "b2b3-sync")
	ctx := context.Background()
	acct := billing.Account{ID: "acct-b2b3-sync", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	adj := billing.AdjustmentInput{AccountID: acct.ID, Amount: billing.Money{Nano: 100, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b3-sync-adj-1", Reason: "correction"}
	posted, err := store.PostAdjustment(ctx, adj)
	if err != nil {
		t.Fatalf("v1_active sync adjustment: %v", err)
	}
	if posted.Replayed {
		t.Fatalf("first must not be replayed")
	}
	replayed, err := store.PostAdjustment(ctx, adj)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("second identical must be replayed")
	}
	m, _ := store.EnsureAccountingCutover(ctx)
	sh, _ := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "b2b3-sync-shadow"})
	_ = sh
	if _, _, err := store.BeginCutoverDraining(ctx, "b2b3-sync-drain"); err != nil {
		t.Fatal(err)
	}
	// Exact replay of pre-drain money remains allowed in draining (no new effect).
	if _, err := store.PostAdjustment(ctx, adj); err != nil {
		t.Fatalf("draining exact replay: %v", err)
	}
	// New sync adjustment in draining fenced with zero effects.
	before, _ := store.GetAccount(ctx, acct.ID)
	fresh := billing.AdjustmentInput{AccountID: acct.ID, Amount: billing.Money{Nano: 50, Currency: "USD"}, Direction: billing.AdjustmentCredit, SourceKey: "b2b3-sync-adj-2", Reason: "correction"}
	if _, err := store.PostAdjustment(ctx, fresh); err == nil {
		t.Fatalf("draining new sync adjustment must be fenced")
	} else if !b2b3IsFenceErr(err) {
		t.Fatalf("draining new err = %v, want fence", err)
	}
	after, _ := store.GetAccount(ctx, acct.ID)
	if after.BalanceNano != before.BalanceNano {
		t.Fatalf("fenced sync adjustment mutated balance %d -> %d", before.BalanceNano, after.BalanceNano)
	}
}
