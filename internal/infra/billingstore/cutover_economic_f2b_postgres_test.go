//go:build integration

package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// Phase 17.3 F2B PostgreSQL parity: monetary economic inventory, evidence-only
// controls, and V2 fencing on configured direct PostgreSQL. Skips unless
// LIP_REQUIRE_POSTGRES=1 and a DSN is configured; the SQLite suite is
// authoritative when PostgreSQL is unavailable.
func TestF2BEconomicPostgresParityWhenConfigured(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "f2b-pg"})
	if err != nil {
		t.Fatalf("NewDurableStore postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	acct := billing.Account{ID: "acct-f2b-pg", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	m, err := store.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: m.Version, ExpectedEpoch: m.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f2b-pg-shadow",
	}); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	work := f2bProviderWork(t, store, acct.ID, callID, "b-pg", "f2b-head-pg", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, work); err != nil {
		t.Fatalf("append pg monetary: %v", err)
	}
	// Lease it to prove leased counts on PG.
	claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "f2b-pg-worker", time.Minute, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("pg claims = %d, want 1", len(claims))
	}
	if err := store.RetryEconomicRevisionWorkWithReason(ctx, claims[0].Work, claims[0].Claim, billing.EconomicWorkReasonTransientFailure, time.Time{}); err != nil {
		t.Fatal(err)
	}
	_, status, err := store.BeginCutoverDraining(ctx, "f2b-pg-drain")
	if err != nil {
		t.Fatal(err)
	}
	if status.ReadyForActivation {
		t.Fatalf("pg drain must block with monetary economic work")
	}
	if status.Counts.EconomicProviderPending == 0 || status.Counts.V1Pinned == 0 {
		t.Fatalf("pg economic=%d pinned=%d, want >0", status.Counts.EconomicProviderPending, status.Counts.V1Pinned)
	}
	// Evidence-only must not block on PG: fresh store with only evidence is ready.
	bunDB2, _ := openIsolatedPostgresBun(t, dsn, 4)
	evStore, err := NewDurableStore(ctx, bunDB2, Config{StoreID: "f2b-pg-ev"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = evStore.Close() })
	evAcct := billing.Account{ID: "acct-f2b-pg-ev", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000000, State: billing.AccountReady, Version: 1}
	if err := evStore.CreateAccount(ctx, evAcct); err != nil {
		t.Fatal(err)
	}
	em, err := evStore.EnsureAccountingCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evStore.TransitionAccountingCutover(ctx, billing.AccountingCutoverTransition{
		ExpectedVersion: em.Version, ExpectedEpoch: em.Epoch,
		NextState: billing.AccountingCutoverV2Shadow, TransitionID: "f2b-pg-ev-shadow",
	}); err != nil {
		t.Fatal(err)
	}
	evCall, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	evCust := f2bCustomerWork(t, evStore, evAcct.ID, evCall, "b-pg-ev", "f2b-pg-ev-cust", 1)
	if err := evStore.AppendEconomicRevisionWork(ctx, evCust); err != nil {
		t.Fatal(err)
	}
	_, evStatus, err := evStore.BeginCutoverDraining(ctx, "f2b-pg-ev-drain")
	if err != nil {
		t.Fatal(err)
	}
	if !evStatus.ReadyForActivation {
		t.Fatalf("pg evidence-only drain must be ready (economic=%d)", evStatus.Counts.EconomicProviderPending)
	}
	// Complete monetary via production worker, then activate on PG.
	worker, err := billing.NewEconomicRevisionWorkerWithReconcilerAndProviderCostWithClaim(
		store, store, f2bRater{}, nil, store, store, billing.EconomicQueueProvider, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("pg worker: %v", err)
	}
	if _, err := store.ActivateCutoverV2(ctx, "f2b-pg-activate"); err != nil {
		t.Fatalf("pg activate after drain: %v", err)
	}
	// V2 fence on PG: new V1 fenced, explicit V2 allowed.
	freshID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	fresh := f2bProviderWork(t, store, acct.ID, freshID, "b-pg-fresh", "f2b-head-pg-fresh", 1, true)
	if err := store.AppendEconomicRevisionWork(ctx, fresh); !f2bIsFenceErr(err) {
		t.Fatalf("pg new V1 in active err = %v, want fence", err)
	}
	v2ID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	v2Work := f2bProviderWork(t, store, acct.ID, v2ID, "b-pg-v2", "f2b-head-pg-v2", 1, true)
	v2Work.PostingOwner = billing.PostingOwnerV2
	v2Work, err = v2Work.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendProviderPostingEconomicRevisionWork(ctx, v2Work, billing.PostingOwnerV2); err != nil {
		t.Fatalf("pg V2 append: %v", err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("pg V2 worker: %v", err)
	}
}
