package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteOperatorCostReportAttributesByCallAndBLeg(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "operator-composite-cogs", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 1000, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	firstID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic B-leg IDs collide across calls; attribution must stay call-scoped.
	appendIndependentLegAt(t, store, account.ID, firstID, "seq_1", time.Unix(100, 0).UTC(), 5)
	appendIndependentLegAt(t, store, account.ID, secondID, "seq_1", time.Unix(200, 0).UTC(), 9)

	report, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: account.ID, Page: billing.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(report.Rows))
	}
	byCall := map[string]billing.OperatorCostRow{}
	for _, row := range report.Rows {
		byCall[row.TurnID] = row
	}
	first := byCall[firstID.String()]
	second := byCall[secondID.String()]
	if first.BLegID != "seq_1" || first.Amount.Nano != 5 {
		t.Fatalf("first call row = %+v, want seq_1 amount=5", first)
	}
	if second.BLegID != "seq_1" || second.Amount.Nano != 9 {
		t.Fatalf("second call row = %+v, want seq_1 amount=9", second)
	}
}
