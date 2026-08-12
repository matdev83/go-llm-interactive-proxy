package billingstore

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLitePhase7BoundedOperatorQueries(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: "operator-queue", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	record := testTUR("operator-queue")
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	processing, err := store.QueryProcessing(ctx, billing.ReportFilter{Page: billing.PageRequest{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(processing.Items) != 1 || processing.Items[0].Status != billing.ProcessingPending || processing.Items[0].TURKey == "" {
		t.Fatalf("processing page = %+v", processing)
	}

	if _, err := store.Authorize(ctx, authorizationInput("operator-queue", "hold-turn", "hold-auth", 10)); err != nil {
		t.Fatal(err)
	}
	holds, err := store.QueryOpenHolds(ctx, "operator-queue", billing.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 1 || holds.Items[0].Status != "open" || holds.Items[0].Amount.Nano != 10 {
		t.Fatalf("hold page = %+v", holds)
	}

	if err := store.MarkAccountReconcileRequired(ctx, "operator-queue"); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.QueryReconcileRequired(ctx, billing.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Items) != 1 || blocked.Items[0].ID != "operator-queue" || blocked.Items[0].State != billing.AccountReconcileRequired {
		t.Fatalf("reconcile page = %+v", blocked)
	}
}

func TestSQLiteQueryProcessingHonorsAccountID(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for _, accountID := range []string{"proc-acct-a", "proc-acct-b"} {
		if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
			t.Fatal(err)
		}
		record := testTUR(accountID)
		record.TurnID = "turn-" + accountID
		if err := store.AppendUsageRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.QueryProcessing(ctx, billing.ReportFilter{AccountID: "proc-acct-a", Page: billing.PageRequest{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	wantKey, err := billing.TURKey("proc-acct-a", "turn-proc-acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].TURKey != wantKey {
		t.Fatalf("processing page = %+v, want only %s", page, wantKey)
	}
}
