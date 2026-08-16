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

func TestProviderCostWorkPostgresBackfillAvoidsTextIntoTimestamptz(t *testing.T) {
	t.Parallel()
	for _, statement := range postgresProviderCostWorkDDL() {
		lower := strings.ToLower(statement)
		if !strings.Contains(lower, "insert into provider_cost_work") {
			continue
		}
		if strings.Contains(lower, "sealed_at") {
			t.Fatalf("PG provider_cost_work backfill must not insert TEXT sealed_at into TIMESTAMPTZ columns: %s", statement)
		}
		if !strings.Contains(lower, "current_timestamp") {
			t.Fatalf("PG provider_cost_work backfill must use CURRENT_TIMESTAMP: %s", statement)
		}
	}
}

func TestSQLiteProviderCostWorkBackfillUsesCurrentTimestamp(t *testing.T) {
	t.Parallel()
	for _, statement := range sqliteProviderCostWorkDDL() {
		lower := strings.ToLower(statement)
		if !strings.Contains(lower, "insert") {
			continue
		}
		if strings.Contains(lower, "sealed_at") {
			t.Fatalf("SQLite provider_cost_work backfill must not copy sealed_at: %s", statement)
		}
	}
}

func TestSQLiteProviderCostWorkInsertWritesNextAttemptAt(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-next")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	var next string
	if err := store.db.NewRaw(`SELECT next_attempt_at FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &next); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(next) == "" {
		t.Fatal("next_attempt_at must be written explicitly on insert")
	}
}

func TestSQLiteDeferProviderCostWorkAtomicUnderConcurrency(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	leg := testIndependentCallLegFor(callID, "b-atomic")
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	work := billing.ProviderCostWork{AccountID: "acct-corr", CallID: callID, Leg: leg}
	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.DeferProviderCostWork(ctx, work, "concurrent")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := leg.Seal()
	if err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := store.db.NewRaw(`SELECT attempt_count FROM provider_cost_work WHERE usage_leg_key = ?`, sealed.Key).Scan(ctx, &attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != goroutines {
		t.Fatalf("attempt_count = %d, want %d", attempts, goroutines)
	}
}

func TestSQLiteDeferUsageAppendAtomicUnderConcurrency(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	call := testOutboxCall(t)
	if err := store.EnqueueCallUsageAppend(ctx, call, "io"); err != nil {
		t.Fatal(err)
	}
	key := mustCallKey(t, call)
	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.DeferUsageAppend(ctx, key, "busy")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	if err := store.db.NewRaw(`SELECT attempt_count FROM usage_append_outbox WHERE append_key = ?`, key).Scan(ctx, &attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != goroutines {
		t.Fatalf("attempt_count = %d, want %d", attempts, goroutines)
	}
}

func TestSQLiteCompleteCallStaleClaimLeaseRecoversWithoutStealingFresh(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	staleID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []billing.BillingCallID{staleID, freshID} {
		if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(id, []string{"b-1"})); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(id, "b-1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ClaimCompleteCall(ctx, staleID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCompleteCall(ctx, freshID); err != nil {
		t.Fatal(err)
	}
	staleClaimedAt := time.Now().UTC().Add(-completeCallClaimLease - time.Minute)
	if _, err := store.db.NewRaw(`UPDATE usage_call_records SET claimed_at = ? WHERE call_id = ?`, staleClaimedAt, staleID.String()).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimCompleteCalls(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Closure.CallID != staleID {
		t.Fatalf("stale reclaim = %+v, want only %s", claimed, staleID)
	}
	if _, err := store.ClaimCompleteCall(ctx, freshID); !errors.Is(err, billing.ErrCallClaimConflict) {
		t.Fatalf("fresh claim steal = %v, want ErrCallClaimConflict", err)
	}
}

func TestSQLiteCompleteCallRetryBackoffHidesUntilDueAndReconcileTerminal(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(callID, []string{"b-1"})); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCompleteCall(ctx, callID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryCompleteCall(ctx, callID, "rating_input"); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	var next time.Time
	if err := store.db.NewRaw(`SELECT claim_status, claim_attempt_count, next_claim_at FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &status, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if status != usageCallClaimPending || attempts != 1 || !next.After(time.Now().UTC()) {
		t.Fatalf("retry state = status %q attempts %d next %v", status, attempts, next)
	}
	claimed, err := store.ClaimCompleteCalls(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("backoff claim visible early = %+v", claimed)
	}

	if _, err := store.ClaimCompleteCall(ctx, callID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryCompleteCall(ctx, callID, "settlement_reconcile_required"); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT claim_status FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != usageCallClaimReconcileRequired {
		t.Fatalf("reconcile status = %q, want %s", status, usageCallClaimReconcileRequired)
	}
	claimed, err = store.ClaimCompleteCalls(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("reconcile_required must not hot-loop via worker claims: %+v", claimed)
	}
	if _, err := store.ClaimCompleteCall(ctx, callID); err != nil {
		t.Fatalf("repair claim from reconcile_required: %v", err)
	}
}

func TestSQLiteCompleteCallCrashReplayWorkerSettlesAfterStaleLease(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "crash-replay", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = account.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 40, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCompleteCall(ctx, callID); err != nil {
		t.Fatal(err)
	}
	// Simulate process crash after claim commit, before settlement.
	if _, err := store.db.NewRaw(`UPDATE usage_call_records SET claimed_at = ? WHERE call_id = ?`, time.Now().UTC().Add(-completeCallClaimLease-time.Second), callID.String()).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	worker, err := billing.NewCallPostUsageWorker(store, store, fixedCallRatingResolver{
		result: billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 10, Currency: "USD"}, Fingerprint: "crash-fp"},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("crash-replay ProcessOnce: %v", err)
	}
	got, err := store.GetCallExposure(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsOpen() {
		t.Fatalf("exposure still open after crash-replay settlement: %+v", got)
	}
	_ = exposure
}

func TestSQLiteAdmitExposureClosedSameFingerprintIsIdempotent(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "closed-replay", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, nil)
	call.AccountID = account.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	input := billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 30, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	}
	exposure, err := store.AdmitExposure(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: call, Exposure: exposure,
		Result: billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 5, Currency: "USD"}, Fingerprint: "settle-fp"},
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AdmitExposure(ctx, input)
	if err != nil {
		t.Fatalf("closed same-fingerprint admit = %v", err)
	}
	if replayed.IsOpen() || replayed.Fingerprint != exposure.Fingerprint {
		t.Fatalf("closed replay = %+v, want closed immutable copy", replayed)
	}
	conflict := input
	conflict.Max.Nano = 31
	if _, err := store.AdmitExposure(ctx, conflict); !errors.Is(err, billing.ErrExposureConflict) {
		t.Fatalf("closed conflicting admit = %v, want ErrExposureConflict", err)
	}
}

func TestSQLiteRepairExposureNoChargeRecoversAfterStaleClaim(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "repair-stale", Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := testIndependentCallUsageFor(callID, []string{"b-1"})
	call.AccountID = account.ID
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(callID, "b-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: account.ID, CallID: callID.String(), Max: billing.Money{Nano: 25, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCompleteCall(ctx, callID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE usage_call_records SET claimed_at = ? WHERE call_id = ?`, time.Now().UTC().Add(-completeCallClaimLease-time.Second), callID.String()).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RepairExposureNoCharge(ctx, callID, "operator-stale-repair"); err != nil {
		t.Fatalf("RepairExposureNoCharge after stale claim: %v", err)
	}
}

type fixedCallRatingResolver struct {
	result billing.CallRatingResult
}

func (f fixedCallRatingResolver) ResolveCallRating(context.Context, billing.CompleteCall, billing.CallExposure) (billing.CallRatingResult, error) {
	return f.result, nil
}
