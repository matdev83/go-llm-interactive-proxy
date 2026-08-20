package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteListPendingProviderCostWorkUsesJoinLimitAndOrder(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	firstID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	firstCall := testIndependentCallUsageFor(firstID, []string{"b-first"})
	secondCall := testIndependentCallUsageFor(secondID, []string{"b-second"})
	if err := store.AppendCallUsage(ctx, firstCall); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.AppendCallUsage(ctx, secondCall); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(firstID, "b-first")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(secondID, "b-second")); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingProviderCostWork(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].CallID != firstID || pending[0].AccountID != firstCall.AccountID {
		t.Fatalf("limited pending work = %+v, want first joined call", pending)
	}
	all, err := store.ListPendingProviderCostWork(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].CallID != secondID {
		t.Fatalf("ordered pending work = %+v", all)
	}
}

func TestSQLiteListPendingProviderCostWorkKeepsOrphanLegRetryable(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-orphan")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingProviderCostWork(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].AccountID != "" || pending[0].CallID != callID {
		t.Fatalf("orphan pending work = %+v, want empty account and retained call id", pending)
	}
}

func TestSQLiteProviderCostWorkPrunesOldProcessedMetadata(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "provider-prune", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-prune")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	result := billing.OperatorCostResult{LURKey: sealed.Key, Amount: billing.Money{Currency: "USD"}, AmountPresent: true, Reconciled: true}
	if _, err := store.ApplyProviderCost(ctx, billing.ApplyProviderCostInput{AccountID: account.ID, CallID: callID, Leg: leg, Result: result}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE provider_cost_work SET updated_at = ? WHERE usage_leg_key = ?`, time.Now().Add(-48*time.Hour), sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPendingProviderCostWork(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("old processed provider work rows = %d, want pruned", count)
	}
}

func TestSQLiteProviderCostWorkBackoffHidesDeferredWorkUntilDue(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(callID, []string{"b-backoff"})); err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-backoff")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	work := billing.ProviderCostWork{AccountID: "acct-corr", CallID: callID, Leg: leg}
	if err := store.DeferProviderCostWork(ctx, work, "missing operator rate"); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var next time.Time
	var reason string
	if err := store.db.NewRaw(`SELECT attempt_count, next_attempt_at, last_error FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &attempts, &next, &reason); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !next.After(time.Now()) || reason != "missing operator rate" {
		t.Fatalf("retry state = attempts %d next %s reason %q", attempts, next, reason)
	}
	pending, err := store.ListPendingProviderCostWork(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("deferred work visible before next_attempt_at: %+v", pending)
	}
	if _, err := store.db.NewRaw(`UPDATE provider_cost_work SET next_attempt_at = ? WHERE usage_leg_key = ?`, time.Now().Add(-time.Second), sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListPendingProviderCostWork(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("due deferred work = %+v, want one item", pending)
	}
}

func TestSQLiteProviderCostWorkSchemaBackfillsMissingQueueRows(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-backfill")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`DELETE FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := providerCostWorkSchemaUp(ctx, store.db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM provider_cost_work WHERE usage_leg_key = ? AND status = 'pending'`, sealed.Key).Scan(ctx, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("backfilled provider-cost work rows = %d, want 1", count)
	}
}

func TestSQLiteProviderCostWorkRetryMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	if err := providerCostWorkRetrySchemaUp(context.Background(), store.db); err != nil {
		t.Fatal(err)
	}
	if err := providerCostWorkRetrySchemaUp(context.Background(), store.db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('provider_cost_work') WHERE name = 'attempt_count'`).Scan(context.Background(), &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("attempt_count columns = %d, want 1", count)
	}
}
