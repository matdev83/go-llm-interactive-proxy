//go:build integration

package billingstore

// Phase 17.3 F5+F7 PostgreSQL parity: revision-specific immutable pins +
// atomic handoff on configured direct PostgreSQL. Skips unless
// LIP_REQUIRE_POSTGRES=1 and DSN configured; SQLite suite authoritative otherwise.

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestF5F7PostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f5f7-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-f5f7-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	base := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-pg-head", 1, 20, true)
	if _, err := store.ApplyProviderCostRevision(ctx, base); err != nil {
		t.Fatalf("pg V1 base: %v", err)
	}
	// Activate to v2_active via production transitions.
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch, NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f5f7-pg-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	dr, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: sh.Version, ExpectedEpoch: sh.Epoch, NextState: billing.AccountingCutoverV1Draining, TransitionID: "f5f7-pg-drain"})
	if err != nil {
		t.Fatal(err)
	}
	_ = dr
	cur, err := store.GetAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{ExpectedVersion: cur.Version, ExpectedEpoch: cur.Epoch, NextState: billing.AccountingCutoverV2Active, TransitionID: "f5f7-pg-active"}); err != nil {
		t.Fatal(err)
	}
	// Higher V1 must fence.
	higherV1 := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-pg-head", 2, 99, true)
	if _, err := store.ApplyProviderCostRevision(ctx, higherV1); !f5f7IsFenceErr(err) {
		t.Fatalf("pg higher V1 must fence, err=%v", err)
	}
	// Exact V1 replay succeeds.
	if replay, err := store.ApplyProviderCostRevision(ctx, base); err != nil {
		t.Fatalf("pg exact replay: %v", err)
	} else if !replay.Replayed {
		t.Fatalf("pg replay must be replayed")
	}
	// V2 higher posts.
	v2 := f5f7RevisionInput(t, store, acct.ID, callID, "f5f7-pg-head", 2, 35, true)
	v2.PostingOwner = billing.PostingOwnerV2
	if posted, err := store.ApplyProviderCostRevision(ctx, v2); err != nil {
		t.Fatalf("pg V2 higher: %v", err)
	} else if !posted.Applied {
		t.Fatalf("pg V2 must apply")
	}
	// Revision pin distinct + completed, history immutable.
	v2Key, err := billing.ProviderRevisionPostingOperationKey(v2)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := store.GetPostingPin(ctx, billing.PostingOperationProviderCharge, v2Key)
	if err != nil {
		t.Fatalf("pg V2 pin: %v", err)
	}
	if pin.Owner != billing.PostingOwnerV2 || !pin.IsCompleted() {
		t.Fatalf("pg V2 pin must be V2 completed, got %#v", pin)
	}
	if err := VerifySchema(ctx, store.DB()); err != nil {
		t.Fatalf("VerifySchema postgres F5F7: %v", err)
	}
}
