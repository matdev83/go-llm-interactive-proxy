package billingstore

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteProviderCostWorkIsEnqueuedAndCompleted(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-work", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-work")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.NewRaw(`SELECT status FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("new provider cost work status = %q, want pending", status)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Currency: "USD"}, AmountPresent: true, Reconciled: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT status FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "processed" {
		t.Fatalf("completed provider cost work status = %q, want processed", status)
	}
	pending, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending provider cost work after completion = %d, want 0", len(pending))
	}
}

func TestSQLiteApplyProviderCostIsIndependentAndIdempotent(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-cost", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-cost")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: "", Amount: billing.Money{Nano: 11, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result.LURKey = sealed.Key
	posting, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if posting.Replayed || posting.Transaction.OperationKind != "provider_call_cogs" {
		t.Fatalf("provider posting = %+v", posting)
	}
	gotAccount, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAccount.BalanceNano != account.BalanceNano {
		t.Fatalf("provider cost mutated customer account: before=%+v after=%+v", account, gotAccount)
	}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatalf("identical provider replay: %v", err)
	}
	var workStatus string
	if err := store.db.NewRaw(`SELECT status FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &workStatus); err != nil {
		t.Fatal(err)
	}
	if workStatus != "processed" {
		t.Fatalf("replayed provider work status = %q, want processed", workStatus)
	}
	conflict := result
	conflict.Amount.Nano++
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: conflict}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting provider replay = %v, want ErrOperationConflict", err)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cogs int
	for _, transaction := range transactions {
		if transaction.OperationKind == "provider_call_cogs" {
			cogs++
		}
	}
	if cogs != 1 {
		t.Fatalf("provider COGS journal count = %d, want 1", cogs)
	}
}

func TestSQLiteUnreconciledProviderCostStoresIndependentRetryMarker(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-unreconciled", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-unreconciled")
	if err := store.MarkProviderCostUnreconciled(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg}, "missing operator rate"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProviderCostUnreconciled(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg}, "missing operator rate"); err != nil {
		t.Fatalf("identical unreconciled replay: %v", err)
	}
	var markers int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE account_id = ? AND operation_kind = 'provider_cost_unreconciled'`, account.ID).Scan(ctx, &markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("unreconciled provider markers = %d, want 1", markers)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != account.BalanceNano || got.Version != account.Version {
		t.Fatalf("unreconciled marker mutated account: before=%+v after=%+v", account, got)
	}
}

func TestSQLiteApplyProviderCostExactZeroStoresOperationMarker(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-zero", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-zero")
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Currency: "USD"}, AmountPresent: true, Reconciled: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatal(err)
	}
	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, transaction := range transactions {
		if transaction.OperationKind == "provider_call_cogs" {
			t.Fatalf("zero provider cost wrote journal transaction: %+v", transaction)
		}
	}
	var markers int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM billing_operation_snapshots WHERE operation_kind = 'provider_call_cogs' AND source_key = ?`, sealed.Key).Scan(ctx, &markers); err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("zero provider operation markers = %d, want 1", markers)
	}
}

func TestSQLiteApplyProviderCostAllowsReconcileRequiredAccount(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-reconcile", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReconcileRequired, Version: 4}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-reconcile")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 17, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	posting, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result})
	if err != nil {
		t.Fatalf("ApplyProviderCost under reconcile_required: %v", err)
	}
	if posting.Transaction.OperationKind != "provider_call_cogs" || posting.Transaction.Entries[0].Amount.Nano != 17 {
		t.Fatalf("provider posting = %+v", posting)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != account.BalanceNano || got.Version != account.Version || got.State != billing.AccountReconcileRequired {
		t.Fatalf("customer account mutated: before=%+v after=%+v", account, got)
	}
}

func TestSQLiteApplyProviderCostClearsUnreconciledMarker(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-clear-unrec", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-clear")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProviderCostUnreconciled(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg}, "missing operator rate"); err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 9, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatal(err)
	}
	var openMarkers int
	if err := store.db.NewRaw(`
SELECT COUNT(1) FROM billing_operation_snapshots u
WHERE u.account_id = ? AND u.operation_kind = 'provider_cost_unreconciled' AND u.source_key = ?
AND NOT EXISTS (
	SELECT 1 FROM billing_operation_snapshots c
	WHERE c.account_id = u.account_id AND c.operation_kind = 'provider_call_cogs' AND c.source_key = u.source_key
)`, account.ID, sealed.Key).Scan(ctx, &openMarkers); err != nil {
		t.Fatal(err)
	}
	if openMarkers != 0 {
		t.Fatalf("open unreconciled markers after apply = %d, want 0", openMarkers)
	}
	report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if report.UnreconciledCosts != 0 {
		t.Fatalf("OperatorCostReport.UnreconciledCosts = %d, want 0", report.UnreconciledCosts)
	}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatalf("exact provider replay after clear: %v", err)
	}
}
