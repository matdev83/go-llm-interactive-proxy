package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteConcurrentSettlementIsIdempotent(t *testing.T) {
	runConcurrentSettlementIdempotent(t, newSQLiteTestStore(t), "concurrent-settlement")
}

func TestSQLiteSettlementFailsClosedWhenProcessedWithoutSnapshot(t *testing.T) {
	runSettlementFailsClosedWhenProcessedWithoutSnapshot(t, newSQLiteTestStore(t), "processed-nosnap")
}

func TestSQLiteStaleClaimerCannotSettleAfterLeaseReclaim(t *testing.T) {
	ownerA := newSQLiteTestStore(t)
	ownerB := newSQLiteSiblingStore(t, ownerA, "settle-worker-b")
	runStaleClaimerCannotSettleAfterLeaseReclaim(t, ownerA, ownerB, "stale-settle")
}

func runConcurrentSettlementIdempotent(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 3, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	input := billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 11, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true}}}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, callErr := store.ApplyBillingResult(ctx, input)
			errs <- callErr
		})
	}
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent settlement error = %v", callErr)
		}
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 89 || account.ReservedNano != 0 {
		t.Fatalf("concurrent account = %+v", account)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 4 {
		t.Fatalf("concurrent journal count = %d, want authorization/customer/provider/release", len(journals))
	}
}

func runSettlementFailsClosedWhenProcessedWithoutSnapshot(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 3, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessingProcessed(ctx, sealed.Key, sealed.Fingerprint, "manual"); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	input := billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 11, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true}}}}
	if _, err := store.ApplyBillingResult(ctx, input); !errors.Is(err, billing.ErrSettlementConflict) {
		t.Fatalf("settlement without snapshot = %v, want conflict", err)
	}
	after, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceNano != before.BalanceNano || after.ReservedNano != before.ReservedNano || after.Version != before.Version {
		t.Fatalf("fail-closed must not mutate money: before=%+v after=%+v", before, after)
	}
}

func runStaleClaimerCannotSettleAfterLeaseReclaim(t *testing.T, ownerA, ownerB *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, ownerA)
	ctx := context.Background()
	if err := ownerA.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := ownerA.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 3, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := ownerA.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerA.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if _, err := ownerA.db.NewRaw(`UPDATE usage_record_processing SET lease_until = ?, updated_at = ? WHERE tur_key = ?`, old, old, sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerB.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	input := billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: billing.BillingResult{TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 11, Currency: "USD"}, OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 3, Currency: "USD"}, AmountPresent: true, Reconciled: true}}}}
	if _, err := ownerA.ApplyBillingResult(ctx, input); !errors.Is(err, billing.ErrSettlementConflict) {
		t.Fatalf("stale settlement = %v, want conflict", err)
	}
	if _, err := ownerB.ApplyBillingResult(ctx, input); err != nil {
		t.Fatal(err)
	}
	account, err := ownerB.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 89 || account.ReservedNano != 0 {
		t.Fatalf("owner settlement account = %+v", account)
	}
}
