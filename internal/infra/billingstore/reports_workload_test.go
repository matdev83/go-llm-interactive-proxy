package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteOperatorCostReportProjectsAuxiliaryWorkload(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "operator-workload", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	workload, err := billing.WorkloadIdentityFromAuxiliaryRole(billing.WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID,
		ALegID: "aux-a", SessionID: "aux-session", StartedAt: now.Add(-time.Second), FinishedAt: now,
		Outcome: billing.TurnOutcomeCompleted, ExpectedBLegIDs: []string{"aux-b"}, Workload: workload,
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "aux-b")
	leg.ALegID = "aux-a"
	leg.StartedAt = now.Add(-time.Second)
	leg.FinishedAt = now
	leg.Workload = workload
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{
		AccountID: account.ID, CallID: callID, Leg: leg,
		Result: billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Nano: 7, Currency: "USD"}, AmountPresent: true, Reconciled: true, Authoritative: true},
	}); err != nil {
		t.Fatal(err)
	}
	report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 || report.Rows[0].Workload != workload {
		t.Fatalf("operator rows=%+v, want one auxiliary workload=%+v", report.Rows, workload)
	}
	if report.Rows[0].Amount.Nano != 7 {
		t.Fatalf("workload projection changed provider price: amount=%+v", report.Rows[0].Amount)
	}
}
