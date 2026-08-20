package billingstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func TestSQLiteBillingStoreContract(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	runBillingStoreContract(t, store, "contract-sqlite")
}

// runBillingStoreContract is the dialect-shared foundation suite.
// SQLite unit tests and PostgreSQL integration tests must exercise the same
// invariants: balanced posting, replay, correction, exposure admission,
// independent usage append, call settlement, locking/versioning, concurrent
// AccountSequence allocation, and crash rollback.
func runBillingStoreContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPostpaid, CreditLimit: 1000, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := VerifySchema(ctx, store.db); err != nil {
		t.Fatalf("VerifySchema: %v", err)
	}

	input := billing.JournalTransaction{
		ID: accountID + "-tx-1", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: accountID + "-source-1", AccountID: accountID,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "cash", Side: billing.JournalDebit, Amount: billing.Money{Nano: 25, Currency: "USD"}},
			{LedgerAccount: "customer", Side: billing.JournalCredit, Amount: billing.Money{Nano: 25, Currency: "USD"}},
		},
	}
	posted, err := store.postJournalTransaction(ctx, input)
	if err != nil {
		t.Fatalf("PostJournalTransaction: %v", err)
	}
	if posted.AccountSequence != 1 {
		t.Fatalf("sequence = %d, want 1", posted.AccountSequence)
	}
	if !strings.HasPrefix(posted.SemanticFingerprint, billing.JournalFingerprintPrefix) {
		t.Fatalf("posted fingerprint %q missing version prefix", posted.SemanticFingerprint)
	}
	if _, err := store.postJournalTransaction(ctx, input); err != nil {
		t.Fatalf("same journal replay: %v", err)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil || len(journals) != 1 || journals[0].AccountSequence != 1 {
		t.Fatalf("journal replay rows = %#v err=%v; want one sequence-1 row", journals, err)
	}

	// Correction linkage: reversal then replacement; orphan replacement rejected.
	reversalInput := billing.JournalTransaction{
		ID: accountID + "-tx-reversal", Book: billing.JournalBookFinancial, Currency: "USD",
		SourceKey: accountID + "-source-reversal", AccountID: accountID, ReversalOf: posted.ID,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer", Side: billing.JournalDebit, Amount: billing.Money{Nano: 25, Currency: "USD"}},
			{LedgerAccount: "cash", Side: billing.JournalCredit, Amount: billing.Money{Nano: 25, Currency: "USD"}},
		},
	}
	reversal, err := store.postJournalTransaction(ctx, reversalInput)
	if err != nil {
		t.Fatalf("reversal: %v", err)
	}
	if reversal.CorrectionGroupID != posted.ID || reversal.ReversalOf != posted.ID || reversal.AccountSequence != 2 {
		t.Fatalf("reversal = %#v, want group/original %q sequence 2", reversal, posted.ID)
	}
	fresh, err := store.postJournalTransaction(ctx, billing.JournalTransaction{
		ID: accountID + "-tx-fresh", Book: billing.JournalBookFinancial, Currency: "USD",
		SourceKey: accountID + "-source-fresh", AccountID: accountID,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "cash", Side: billing.JournalDebit, Amount: billing.Money{Nano: 5, Currency: "USD"}},
			{LedgerAccount: "customer", Side: billing.JournalCredit, Amount: billing.Money{Nano: 5, Currency: "USD"}},
		},
	})
	if err != nil {
		t.Fatalf("fresh original: %v", err)
	}
	orphan := billing.JournalTransaction{
		ID: accountID + "-tx-orphan", Book: billing.JournalBookFinancial, Currency: "USD",
		SourceKey: accountID + "-source-orphan", AccountID: accountID, CorrectsTransactionID: fresh.ID,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "cash", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "customer", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}
	if _, err := store.postJournalTransaction(ctx, orphan); !errors.Is(err, ErrCorrectionInvalid) {
		t.Fatalf("replacement without reversal = %v, want ErrCorrectionInvalid", err)
	}
	replacementInput := billing.JournalTransaction{
		ID: accountID + "-tx-replacement", Book: billing.JournalBookFinancial, Currency: "USD",
		SourceKey: accountID + "-source-replacement", AccountID: accountID, CorrectsTransactionID: posted.ID,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "cash", Side: billing.JournalDebit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
			{LedgerAccount: "customer", Side: billing.JournalCredit, Amount: billing.Money{Nano: 20, Currency: "USD"}},
		},
	}
	replacement, err := store.postJournalTransaction(ctx, replacementInput)
	if err != nil {
		t.Fatalf("replacement: %v", err)
	}
	if replacement.CorrectionGroupID != reversal.CorrectionGroupID || replacement.CorrectsTransactionID != posted.ID {
		t.Fatalf("replacement = %#v, want group %q", replacement, reversal.CorrectionGroupID)
	}

	// Exposure / independent usage / call settlement are the authoritative path.
	runBillingStoreExposureAdmissionContract(t, store, accountID+"-exposure")
	runBillingStoreIndependentUsageContract(t, store, accountID+"-usage")
	runBillingStoreCallSettlementContract(t, store, accountID+"-settle")

	runBillingStoreConcurrentSequenceContract(t, store, accountID+"-seq")
	runBillingStoreCrashRollbackContract(t, store, accountID+"-rollback")
}

func runBillingStoreConcurrentSequenceContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, BalanceNano: 100000, Version: 1}); err != nil {
		t.Fatal(err)
	}
	const count = 12
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := billing.JournalTransaction{
				ID: accountID + "-tx-" + itoa(i), Book: billing.JournalBookFinancial, Currency: "USD",
				SourceKey: accountID + "-source-" + itoa(i), AccountID: accountID,
				Entries: []billing.JournalEntry{
					{LedgerAccount: "customer", Side: billing.JournalDebit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
					{LedgerAccount: "revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 1, Currency: "USD"}},
				},
			}
			_, err := store.postJournalTransaction(ctx, input)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent post: %v", err)
		}
	}
	rows, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != count {
		t.Fatalf("journal row count = %d, want %d", len(rows), count)
	}
	sequences := make([]uint64, len(rows))
	for i, row := range rows {
		sequences[i] = row.AccountSequence
	}
	slices.Sort(sequences)
	for i, sequence := range sequences {
		if sequence != uint64(i+1) {
			t.Fatalf("sequence[%d] = %d, want %d; all=%v", i, sequence, i+1, sequences)
		}
	}
}

func runBillingStoreCrashRollbackContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, BalanceNano: 1000, Version: 1}); err != nil {
		t.Fatal(err)
	}
	occupiedID := accountID + "-tx-occupied"
	if _, err := store.db.NewRaw(`
INSERT INTO journal_transactions(
 transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
 reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, occupiedID, accountID, "financial", "USD", accountID+"-occupied-source", "seed",
		"", "", "", 1, "", "", "", "", 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(ctx); err != nil {
		t.Fatalf("seed occupied id: %v", err)
	}
	input := billing.JournalTransaction{
		ID: occupiedID, Book: billing.JournalBookFinancial, Currency: "USD",
		SourceKey: accountID + "-fresh-source", AccountID: accountID,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer", Side: billing.JournalDebit, Amount: billing.Money{Nano: 3, Currency: "USD"}},
			{LedgerAccount: "revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: 3, Currency: "USD"}},
		},
	}
	if _, err := store.postJournalTransaction(ctx, input); err == nil {
		t.Fatal("expected primary-key failure")
	}
	var entryCount int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_entries WHERE transaction_id = ?`, occupiedID).Scan(ctx, &entryCount); err != nil {
		t.Fatal(err)
	}
	if entryCount != 0 {
		t.Fatalf("partial journal_entries = %d, want 0 after rollback", entryCount)
	}
	var sourceCount int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_transactions WHERE source_key = ?`, accountID+"-fresh-source").Scan(ctx, &sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 {
		t.Fatalf("rolled-back source_key still present (%d rows)", sourceCount)
	}
}

func runBillingStoreExposureAdmissionContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	account := billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	input := billing.AdmitExposureInput{
		AccountID: accountID, CallID: "bc_" + accountID + "_1", Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
	}
	got, err := store.AdmitExposure(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsOpen() || got.Max.Nano != 60 {
		t.Fatalf("exposure = %+v", got)
	}
	unchanged, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.BalanceNano != 100 {
		t.Fatalf("admit mutated money: %+v", unchanged)
	}
	if _, err := store.AdmitExposure(ctx, input); err != nil {
		t.Fatalf("identical exposure replay: %v", err)
	}
	conflict := input
	conflict.Max.Nano = 61
	if _, err := store.AdmitExposure(ctx, conflict); !errors.Is(err, billing.ErrExposureConflict) {
		t.Fatalf("conflicting exposure replay = %v, want ErrExposureConflict", err)
	}

	const count = 2
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			_, err := store.AdmitExposure(context.Background(), billing.AdmitExposureInput{
				AccountID: accountID, CallID: "bc_" + accountID + "_c" + itoa(i), Max: billing.Money{Nano: 60, Currency: "USD"},
				PricingRef: billing.VersionRef{ID: "pricing", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v1"},
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	admitted := 0
	for err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, billing.ErrExposureInsufficient):
		default:
			t.Fatalf("concurrent exposure admit = %v", err)
		}
	}
	if admitted != 0 {
		// First admit already used 60 of 100; concurrent 60+60 can admit at most one more (40 left => zero).
		t.Fatalf("concurrent admits after prior 60 = %d, want 0", admitted)
	}
}

func runBillingStoreIndependentUsageContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 50, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: "a-1", SessionID: "s-1",
		StartedAt: time.Unix(10, 0).UTC(), FinishedAt: time.Unix(11, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs: []string{"b-1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatalf("call usage replay: %v", err)
	}
	conflict := call
	conflict.Outcome = billing.TurnOutcomeFailed
	if err := store.AppendCallUsage(ctx, conflict); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("call usage conflict = %v, want ErrReplayConflict", err)
	}
	leg := billing.CallLegUsageRecord{
		CallID: callID, ALegID: "a-1", BLegID: "b-1",
		BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(10, 0).UTC(), FinishedAt: time.Unix(11, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	}
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, leg); err != nil {
		t.Fatalf("leg usage replay: %v", err)
	}
	before, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	journals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if before.BalanceNano != 50 || len(journals) != 0 {
		t.Fatalf("usage append mutated money: account=%+v journals=%d", before, len(journals))
	}
}

func runBillingStoreCallSettlementContract(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateAccount(ctx, billing.Account{ID: accountID, Currency: "USD", Mode: billing.AccountPrepaid, BalanceNano: 100, State: billing.AccountReady, Version: 1}); err != nil {
		t.Fatal(err)
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	call := billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion, CallID: callID, AccountID: accountID, ALegID: "a-settle",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{ID: "prices", Version: "v1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "v2"},
		ExpectedBLegIDs: []string{"b-1"},
	}
	if err := store.AppendCallUsage(ctx, call); err != nil {
		t.Fatal(err)
	}
	exposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: callID.String(), Max: billing.Money{Nano: 60, Currency: "USD"},
		PricingRef: call.CustomerPricingRef, ChargePolicyRef: call.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := billing.CallRatingResult{CallID: callID, CustomerCharge: billing.Money{Nano: 25, Currency: "USD"}, Fingerprint: accountID + "-fp"}
	if _, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BalanceNano != 75 {
		t.Fatalf("balance after settlement = %d, want 75", got.BalanceNano)
	}
	var status string
	if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, callID.String()).Scan(ctx, &status); err != nil {
		t.Fatal(err)
	}
	if status != "closed" {
		t.Fatalf("exposure status = %q, want closed", status)
	}
	replay, err := store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{Call: call, Exposure: exposure, Result: result})
	if err != nil {
		t.Fatalf("settlement replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("settlement replay = %+v, want Replayed", replay)
	}

	overID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	overCall := call
	overCall.CallID = overID
	overCall.ALegID = "a-over"
	if err := store.AppendCallUsage(ctx, overCall); err != nil {
		t.Fatal(err)
	}
	overExposure, err := store.AdmitExposure(ctx, billing.AdmitExposureInput{
		AccountID: accountID, CallID: overID.String(), Max: billing.Money{Nano: 10, Currency: "USD"},
		PricingRef: overCall.CustomerPricingRef, ChargePolicyRef: overCall.ChargePolicyRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyCallBillingResult(ctx, billing.ApplyCallBillingInput{
		Call: overCall, Exposure: overExposure,
		Result: billing.CallRatingResult{CallID: overID, CustomerCharge: billing.Money{Nano: 11, Currency: "USD"}, Fingerprint: accountID + "-over"},
	})
	if !errors.Is(err, billing.ErrSettlementReconcileRequired) {
		t.Fatalf("actual>max = %v, want ErrSettlementReconcileRequired", err)
	}
	overAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if overAccount.State != billing.AccountReconcileRequired {
		t.Fatalf("state = %s, want reconcile_required", overAccount.State)
	}
	var overStatus string
	if err := store.db.NewRaw(`SELECT status FROM call_exposures WHERE call_id = ?`, overID.String()).Scan(ctx, &overStatus); err != nil {
		t.Fatal(err)
	}
	if overStatus != "open" {
		t.Fatalf("over-max exposure status = %q, want open", overStatus)
	}
}
