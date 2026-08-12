package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteInvariantFailureQuarantinesAccountAndLeavesHoldOpen(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	accountID := "invariant-account"
	if err := store.CreateAccount(ctx, billing.Account{
		ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid,
		BalanceNano: 100, State: billing.AccountReady, Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authorize(ctx, authorizationInput(accountID, "tur-turn", "tur-auth", 40)); err != nil {
		t.Fatal(err)
	}
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPending(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	if err := store.MarkProcessingInvariantFailure(ctx, sealed, "billing_invariant"); err != nil {
		t.Fatal(err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.State != billing.AccountReconcileRequired {
		t.Fatalf("state = %s, want reconcile_required", account.State)
	}
	if account.ReservedNano != 40 {
		t.Fatalf("reserved = %d, want open hold 40", account.ReservedNano)
	}
	processing, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != billing.ProcessingTerminalError || processing.SafeErrorCode != "billing_invariant" {
		t.Fatalf("processing = %+v", processing)
	}
	holds, err := store.QueryOpenHolds(ctx, accountID, billing.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(holds.Items) != 1 {
		t.Fatalf("open holds = %d, want 1", len(holds.Items))
	}
	if _, err := store.Authorize(ctx, authorizationInput(accountID, "next-turn", "next-auth", 1)); !errors.Is(err, billing.ErrAccountNotReady) {
		t.Fatalf("new authorize = %v, want ErrAccountNotReady", err)
	}
}

func TestSQLiteProcessingClaimRetryAndProcessedTransitions(t *testing.T) {
	runProcessingClaimRetryAndProcessed(t, newSQLiteTestStore(t), "processing-account")
}

func TestSQLiteRatingMarksOnlyProcessingStateUnreconciled(t *testing.T) {
	runRatingMarksOnlyProcessingStateUnreconciled(t, newSQLiteTestStore(t), "rating-processing")
}

func TestSQLiteProcessingReclaimsExpiredLeaseAndRejectsFingerprintConflict(t *testing.T) {
	runProcessingReclaimsExpiredLeaseAndRejectsFingerprintConflict(t, newSQLiteTestStore(t), "stale-processing")
}

func TestSQLiteClaimPendingIsExclusiveUnderConcurrency(t *testing.T) {
	runClaimPendingIsExclusiveUnderConcurrency(t, newSQLiteTestStore(t), "exclusive-claim")
}

func TestSQLiteStaleClaimerCannotMarkAfterLeaseReclaim(t *testing.T) {
	ownerA := newSQLiteTestStore(t)
	ownerB := newSQLiteSiblingStore(t, ownerA, "worker-b")
	runStaleClaimerCannotMarkAfterLeaseReclaim(t, ownerA, ownerB, "stale-mark")
}

func TestPhase4_ClaimCrashOnCorruptPayloadRollsBackLease(t *testing.T) {
	runClaimCrashOnCorruptPayloadRollsBackLease(t, newSQLiteTestStore(t), "corrupt-claim")
}

func TestSQLiteClaimPendingBatchesMultipleRecordsAtomically(t *testing.T) {
	runClaimPendingBatchesMultipleRecordsAtomically(t, newSQLiteTestStore(t), "batch-claim")
}

func drainPendingProcessing(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	for range 32 {
		claimed, err := store.ClaimPending(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if len(claimed) == 0 {
			return
		}
		for _, record := range claimed {
			if err := store.MarkProcessingTerminal(ctx, record.Key, record.Fingerprint, "parity_drain"); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("pending processing queue did not drain")
}

func runProcessingClaimRetryAndProcessed(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	journalAccount(t, store, accountID)
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPending(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Key != sealed.Key {
		t.Fatalf("claimed = %#v", claimed)
	}
	state, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingProcessing || state.LeaseOwner != store.StoreID() || state.LeaseUntil.IsZero() {
		t.Fatalf("claim state = %+v", state)
	}
	if err := store.MarkProcessingRetryable(ctx, sealed.Key, sealed.Fingerprint, "temporary_store"); err != nil {
		t.Fatal(err)
	}
	state, err = store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingRetryable || state.RetryCount != 1 || state.LeaseOwner != "" {
		t.Fatalf("retry state = %+v", state)
	}
	claimed, err = store.ClaimPending(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("retry claim = %d, %v", len(claimed), err)
	}
	if err := store.MarkProcessingProcessed(ctx, sealed.Key, sealed.Fingerprint, "billing-result-1"); err != nil {
		t.Fatal(err)
	}
	state, err = store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingProcessed || state.ResultRef != "billing-result-1" {
		t.Fatalf("processed state = %+v", state)
	}
	if _, err := store.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

func runRatingMarksOnlyProcessingStateUnreconciled(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	journalAccount(t, store, accountID)
	record := testTUR(accountID)
	record.CustomerPricingRef = billing.VersionRef{ID: "pricing", Version: "v1"}
	record.ChargePolicyRef = billing.VersionRef{ID: "policy", Version: "v1"}
	record.Legs[0].Evidence.InputTokens = billing.Quantity{Value: 1, Present: true}
	record.Legs[0].Evidence.OutputTokens = billing.Quantity{Value: 1, Present: true}
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	pricing := billing.PricingSnapshot{Ref: record.CustomerPricingRef, Currency: "USD", InputPerMillionNano: 1, OutputPerMillionNano: 1, InputRatePresent: true, OutputRatePresent: true}
	policy := billing.ChargePolicy{Ref: record.ChargePolicyRef, PricingRef: pricing.Ref, Scope: billing.ChargeSurfacedTurn, IncludeInputTokens: true, IncludeOutputTokens: true}
	authorization := billing.Authorization{ID: record.AuthorizationID, AccountID: record.AccountID, TURKey: sealed.Key, Amount: billing.Money{Nano: 100, Currency: "USD"}, PricingRef: pricing.Ref, ChargePolicyRef: policy.Ref}
	result, err := billing.RateTurnAndMarkProcessing(ctx, store, billing.RatingInput{Record: record, Authorization: authorization, CustomerPricing: pricing, CustomerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if !result.UnreconciledCost {
		t.Fatal("missing operator cost must block processing")
	}
	state, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingUnreconciledCost || state.SafeErrorCode != "cost_unresolved" {
		t.Fatalf("processing state = %+v", state)
	}
	if state.LeaseUntil.IsZero() || !state.LeaseUntil.After(time.Now().UTC()) {
		t.Fatalf("unreconciled_cost must carry a future backoff lease, got %+v", state)
	}
	loaded, err := store.GetUsageRecord(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint != sealed.Fingerprint || loaded.Legs[0].Evidence.InputTokens.Value != 1 {
		t.Fatalf("sealed record changed = %+v", loaded)
	}
	immediate, err := store.ClaimPending(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(immediate) != 0 {
		t.Fatalf("unreconciled_cost must respect backoff lease; claimed %d", len(immediate))
	}
	old := time.Now().UTC().Add(-time.Second)
	if _, err := store.db.NewRaw(`UPDATE usage_record_processing SET lease_until = ?, updated_at = ? WHERE tur_key = ?`, old, old, sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.ClaimPending(ctx, 1)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Key != sealed.Key {
		t.Fatalf("unreconciled_cost must be reclaimable after backoff; got %d %v", len(reclaimed), err)
	}
	state, err = store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingProcessing {
		t.Fatalf("reclaimed status = %s, want processing", state.Status)
	}
}

func runProcessingReclaimsExpiredLeaseAndRejectsFingerprintConflict(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	journalAccount(t, store, accountID)
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if _, err := store.db.NewRaw(`UPDATE usage_record_processing SET lease_until = ?, updated_at = ? WHERE tur_key = ?`, old, old, sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPending(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("stale claim = %d, %v", len(claimed), err)
	}
	if err := store.MarkProcessingRetryable(ctx, sealed.Key, "different-fingerprint", "bad"); !errors.Is(err, billing.ErrProcessingConflict) {
		t.Fatalf("fingerprint conflict = %v", err)
	}
	if err := store.MarkProcessingTerminal(ctx, sealed.Key, sealed.Fingerprint, "invariant"); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingTerminalError || state.RetryCount != 0 {
		t.Fatalf("terminal state = %+v", state)
	}
	loaded, err := store.GetUsageRecord(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint != sealed.Fingerprint || loaded.Legs[0].ModelID != "model" {
		t.Fatalf("sealed record mutated = %+v", loaded)
	}
}

func runClaimPendingIsExclusiveUnderConcurrency(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	journalAccount(t, store, accountID)
	record := testTUR(accountID)
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	results := make(chan []billing.TurnUsageRecord, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			claimed, err := store.ClaimPending(ctx, 1)
			if err != nil {
				errs <- err
				return
			}
			results <- claimed
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("claim error: %v", err)
	}
	winners := 0
	for claimed := range results {
		if len(claimed) == 1 {
			winners++
		} else if len(claimed) != 0 {
			t.Fatalf("unexpected claim batch size %d", len(claimed))
		}
	}
	if winners != 1 {
		t.Fatalf("exclusive claim winners = %d, want 1", winners)
	}
}

func runStaleClaimerCannotMarkAfterLeaseReclaim(t *testing.T, ownerA, ownerB *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, ownerA)
	ctx := context.Background()
	journalAccount(t, ownerA, accountID)
	record := testTUR(accountID)
	if err := ownerA.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerA.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if _, err := ownerA.db.NewRaw(`UPDATE usage_record_processing SET lease_until = ?, updated_at = ? WHERE tur_key = ?`, old, old, sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := ownerB.ClaimPending(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("reclaim = %d, %v", len(claimed), err)
	}
	if err := ownerA.MarkProcessingTerminal(ctx, sealed.Key, sealed.Fingerprint, "stale"); !errors.Is(err, billing.ErrProcessingLeaseConflict) {
		t.Fatalf("stale mark = %v, want lease conflict", err)
	}
	if err := ownerB.MarkProcessingRetryable(ctx, sealed.Key, sealed.Fingerprint, "owned"); err != nil {
		t.Fatal(err)
	}
	state, err := ownerB.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingRetryable || state.SafeErrorCode != "owned" {
		t.Fatalf("owner mark state = %+v", state)
	}
}

func runClaimCrashOnCorruptPayloadRollsBackLease(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	journalAccount(t, store, accountID)
	now := time.Now().UTC()
	turKey := "tur:" + accountID
	fingerprint := "fp-corrupt"
	if _, err := store.db.NewRaw(`INSERT INTO turn_usage_records(tur_key, fingerprint, schema_version, account_id, turn_id, a_leg_id, authorization_id, started_at, finished_at, outcome, customer_pricing_ref, charge_policy_ref, payload_json, sealed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		turKey, fingerprint, 1, accountID, "turn", "a", "auth", now, now, "ok", "{}", "{}", "{not-json", now).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`INSERT INTO usage_record_processing(tur_key, tur_fingerprint, status, lease_owner, lease_until, retry_count, safe_error_code, result_ref, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		turKey, fingerprint, string(billing.ProcessingPending), "", nil, 0, "", "", now).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPending(ctx, 1); err == nil {
		t.Fatal("corrupt payload must fail claim")
	}
	state, err := store.GetProcessing(ctx, turKey)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != billing.ProcessingPending {
		t.Fatalf("corrupt claim must roll back lease, status=%s", state.Status)
	}
}

func runClaimPendingBatchesMultipleRecordsAtomically(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	drainPendingProcessing(t, store)
	ctx := context.Background()
	journalAccount(t, store, accountID)
	const n = 5
	for i := range n {
		record := testTUR(accountID)
		record.TurnID = "turn-" + itoa(i)
		record.ALegID = "a-" + itoa(i)
		record.AuthorizationID = "auth-" + itoa(i)
		for j := range record.Legs {
			record.Legs[j].ALegID = record.ALegID
		}
		if err := store.AppendUsageRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.ClaimPending(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != n {
		t.Fatalf("claimed = %d, want %d", len(claimed), n)
	}
	seen := make(map[string]struct{}, n)
	for _, record := range claimed {
		if _, ok := seen[record.Key]; ok {
			t.Fatalf("duplicate claim key %q", record.Key)
		}
		seen[record.Key] = struct{}{}
		proc, err := store.GetProcessing(ctx, record.Key)
		if err != nil {
			t.Fatal(err)
		}
		if proc.Status != billing.ProcessingProcessing {
			t.Fatalf("status = %s, want processing", proc.Status)
		}
	}
	again, err := store.ClaimPending(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second claim = %d, want 0", len(again))
	}
}
