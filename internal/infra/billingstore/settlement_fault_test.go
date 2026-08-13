package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestPhase4_SettlementCrashBeforeCommitRollsBackEveryMutation(t *testing.T) {
	runSettlementCrashBeforeCommit(t, newSQLiteTestStore(t), "fault-settlement")
}

func TestPhase4_SettlementCrashAfterEachMutationRollsBack(t *testing.T) {
	runSettlementCrashAfterEachMutation(t, func() *DurableStore { return newSQLiteTestStore(t) }, "fault")
}

func runSettlementCrashBeforeCommit(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	runSettlementCrashAtStage(t, store, accountID, "before_commit")
}

func runSettlementCrashAfterEachMutation(t *testing.T, newStore func() *DurableStore, accountPrefix string) {
	t.Helper()
	stages := []string{
		"after_customer_journal", "after_provider_journal", "after_release_journal",
		"after_hold_close", "after_account_update", "after_snapshot_write",
		"after_processing_update", "before_commit",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			runSettlementCrashAtStage(t, newStore(), accountPrefix+"-"+stage, stage)
		})
	}
}

func runSettlementCrashAtStage(t *testing.T, store *DurableStore, accountID, stage string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.Authorize(ctx, authorizationInput(accountID, "turn", "auth", 40))
	if err != nil {
		t.Fatal(err)
	}
	record := settlementStoreRecord(accountID, "turn", "auth", billing.MoneyEvidence{NanoUnits: 7, Currency: "USD", Present: true})
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	store.settlementFaultHook = func(got string) error {
		if got == stage {
			return errors.New("injected settlement crash at " + stage)
		}
		return nil
	}
	result := billing.Result{
		TURKey: sealed.Key, CustomerCharge: billing.Money{Nano: 12, Currency: "USD"},
		OperatorCosts: []billing.OperatorCostResult{{LURKey: sealed.Legs[0].Key, Amount: billing.Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true}},
	}
	if _, err := store.ApplyBillingResult(ctx, billing.ApplyBillingInput{Record: sealed, Authorization: auth, Result: result}); err == nil {
		t.Fatal("faulted settlement unexpectedly succeeded")
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.BalanceNano != 100 || account.ReservedNano != 40 || account.Version != 2 {
		t.Fatalf("account after %s rollback = %+v", stage, account)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 {
		t.Fatalf("journals after %s rollback = %d, want authorization hold only", stage, len(journals))
	}
	processing, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != billing.ProcessingPending {
		t.Fatalf("processing after %s rollback = %+v", stage, processing)
	}
	var authSnapshots, otherSnapshots int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = ?`, accountID, "authorization").Scan(ctx, &authSnapshots); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind != ?`, accountID, "authorization").Scan(ctx, &otherSnapshots); err != nil {
		t.Fatal(err)
	}
	if authSnapshots != 1 {
		t.Fatalf("authorization snapshots after %s rollback = %d, want 1", stage, authSnapshots)
	}
	if otherSnapshots != 0 {
		t.Fatalf("settlement snapshots after %s rollback = %d, want 0", stage, otherSnapshots)
	}
	var holdStatus string
	if err := store.db.NewRaw(`SELECT status FROM authorization_holds WHERE account_id = ? AND authorization_id = ?`, accountID, "auth").Scan(ctx, &holdStatus); err != nil {
		t.Fatal(err)
	}
	if holdStatus != "open" {
		t.Fatalf("hold after %s rollback = %q, want open", stage, holdStatus)
	}
}
