package billingstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func phase10CostPassThroughStoreFixture(t *testing.T, allowLate bool, posted int64) (*DurableStore, context.Context, billing.Account, billing.CallUsageRecord, billing.CallExposure, billing.CostPassThroughSettlement) {
	t.Helper()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "cost-pass-through", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 200, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: account.ID, ALegID: "a-pass-through",
		SessionID: "session-pass-through", StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 100, Currency: account.Currency},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := billing.CostPassThroughPolicy{
		MissingCost: billing.CostPassThroughMissingCostProvisional,
		SafeBound:   &billing.Money{Nano: 100, Currency: account.Currency}, AllowLateAdjustment: allowLate,
	}
	state := billing.CostPassThroughSettlement{
		PolicyRef: call.ChargePolicyRef, Policy: policy, Status: billing.CostPassThroughSettlementProvisional,
		SafeBound: billing.Money{Nano: 100, Currency: account.Currency}, PostedAmount: billing.Money{Nano: posted, Currency: account.Currency},
	}
	return store, ctx, account, call, exposure, state
}

func phase10ProviderCost(revision uint64, amount int64, currency string) billing.CostPassThroughProviderCost {
	return billing.CostPassThroughProviderCost{
		LURKey: "lur-pass-through", ValuationID: "valuation-" + string(rune('0'+revision)), Revision: revision,
		InputHash: strings.Repeat(string(rune('a'+revision)), 64), Amount: billing.Money{Nano: amount, Currency: currency},
		AmountPresent: true, Reconciled: true, Authoritative: true,
	}
}

func TestSQLitePhase10CostPassThroughRevisionPostsOneDeltaFromCurrentHead(t *testing.T) {
	t.Parallel()
	store, ctx, account, call, exposure, state := phase10CostPassThroughStoreFixture(t, true, 100)
	initial := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "pass-through-provisional", CostPassThrough: &state}
	settled, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: initial})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Customer.Transaction.ID == "" {
		t.Fatal("provisional pass-through settlement did not post a customer transaction")
	}
	initialReplay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: initial})
	if err != nil {
		t.Fatalf("initial pass-through replay: %v", err)
	}
	if !initialReplay.Replayed {
		t.Fatalf("initial pass-through replay = %+v, want replay", initialReplay)
	}

	first, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied || first.Replayed || first.Delta != (billing.Money{Nano: -20, Currency: "USD"}) || first.CurrentAmount != (billing.Money{Nano: 80, Currency: "USD"}) {
		t.Fatalf("first adjustment = %+v, want one -20 delta from 100 to 80", first)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 120 {
		t.Fatalf("balance after first adjustment = %+v, want 120", got)
	}

	replay, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Applied {
		t.Fatalf("identical revision replay = %+v, want replay without application", replay)
	}

	second, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(3, 70, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied || second.Delta != (billing.Money{Nano: -10, Currency: "USD"}) {
		t.Fatalf("superseding revision = %+v, want -10 from current 80 head", second)
	}
	stale, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || stale.Applied {
		t.Fatalf("out-of-order revision = %+v, want stale no-op", stale)
	}
	got, err = store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 130 {
		t.Fatalf("balance after superseding/replayed revisions = %+v, want 130", got)
	}

	transactions, err := store.JournalTransactions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	var adjustments int
	for _, transaction := range transactions {
		if transaction.OperationKind == CostPassThroughAdjustmentOperationKind {
			adjustments++
		}
	}
	if adjustments != 2 {
		t.Fatalf("adjustment journal count = %d, want two deltas", adjustments)
	}
}

func TestSQLitePhase10CostPassThroughPendingPolicyDoesNotLateAdjust(t *testing.T) {
	t.Parallel()
	store, ctx, account, call, exposure, state := phase10CostPassThroughStoreFixture(t, false, 0)
	state.Status = billing.CostPassThroughSettlementPending
	state.Policy.MissingCost = billing.CostPassThroughMissingCostPending
	initial := billing.CallRatingResult{CallID: call.CallID, CustomerCharge: billing.Money{Nano: 0, Currency: "USD"}, Fingerprint: "pass-through-pending", CostPassThrough: &state}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: initial}); err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ignored || result.Applied || result.CurrentAmount.Nano != 0 {
		t.Fatalf("forbidden late adjustment = %+v, want ignored pending state", result)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != account.BalanceNano {
		t.Fatalf("forbidden late adjustment changed balance: %+v", got)
	}
}

func TestSQLitePhase10CostPassThroughRevisionFailsClosedForCurrencyAndConcurrentReplay(t *testing.T) {
	t.Parallel()
	store, ctx, account, call, exposure, state := phase10CostPassThroughStoreFixture(t, true, 100)
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: billing.CallRatingResult{CallID: call.CallID, CustomerCharge: state.PostedAmount, Fingerprint: "pass-through-concurrent", CostPassThrough: &state}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: phase10ProviderCost(2, 80, "EUR")})
	if !errors.Is(err, billing.ErrCostPassThroughCurrencyMismatch) {
		t.Fatalf("currency mismatch = %v, want typed mismatch", err)
	}
	after, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BalanceNano != before.BalanceNano || after.Version != before.Version {
		t.Fatalf("currency mismatch changed account: before=%+v after=%+v", before, after)
	}

	provider := phase10ProviderCost(2, 80, "USD")
	const workers = 8
	results := make([]billing.CostPassThroughRevisionResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = store.ApplyCostPassThroughRevision(ctx, billing.ApplyCostPassThroughRevisionInput{AccountID: account.ID, CallID: call.CallID, ProviderCost: provider})
		}(i)
	}
	wg.Wait()
	for index, workerErr := range errs {
		if workerErr != nil {
			t.Fatalf("concurrent worker %d: %v", index, workerErr)
		}
	}
	var applied, replayed int
	for _, result := range results {
		if result.Applied {
			applied++
		}
		if result.Replayed {
			replayed++
		}
	}
	if applied != 1 || replayed != workers-1 {
		t.Fatalf("concurrent results = applied %d replayed %d, want 1/%d: %+v", applied, replayed, workers-1, results)
	}
	got, err := store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 120 {
		t.Fatalf("concurrent balance = %+v, want one -20 delta", got)
	}
}
