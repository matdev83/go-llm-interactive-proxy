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
	store := newSQLiteTestStore(t)
	runBillingStoreContract(t, store, "contract-sqlite")
}

// runBillingStoreContract is the dialect-shared Phase 2 foundation suite.
// SQLite unit tests and PostgreSQL integration tests must exercise the same
// invariants: balanced posting, replay, correction, snapshots, locking/versioning,
// concurrent AccountSequence allocation, and crash rollback.
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

	tur := billing.TurnUsageRecord{
		SchemaVersion:   billing.CurrentRecordSchemaVersion,
		AccountID:       accountID,
		TurnID:          accountID + "-turn-1",
		ALegID:          accountID + "-a-1",
		AuthorizationID: accountID + "-auth-1",
		StartedAt:       time.Unix(10, 0).UTC(),
		FinishedAt:      time.Unix(11, 0).UTC(),
		Outcome:         billing.TurnOutcomeCompleted,
		Legs: []billing.LegUsageRecord{{
			ALegID: accountID + "-a-1", BLegID: accountID + "-b-1", Seq: 1,
			BackendID: "backend", ProviderID: "provider", ModelID: "model",
			StartedAt: time.Unix(10, 0).UTC(), FinishedAt: time.Unix(11, 0).UTC(),
			Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		}},
	}
	if err := store.AppendUsageRecord(ctx, tur); err != nil {
		t.Fatalf("AppendUsageRecord: %v", err)
	}
	if err := store.AppendUsageRecord(ctx, tur); err != nil {
		t.Fatalf("same TUR replay: %v", err)
	}
	tur.Legs[0].ModelID = "different-model"
	if err := store.AppendUsageRecord(ctx, tur); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("conflicting TUR replay = %v, want ErrReplayConflict", err)
	}

	// Snapshot + account locking/versioning via atomic authorization.
	beforeAuth, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSpendable, err := beforeAuth.SpendableNano()
	if err != nil {
		t.Fatal(err)
	}
	authorization, authErr := store.Authorize(ctx, authorizationInput(accountID, "authorization-turn", accountID+"-authorization", 10))
	if authErr != nil {
		t.Fatalf("Authorize: %v", authErr)
	}
	if authorization.Before.SpendableNano != beforeSpendable {
		t.Fatalf("authorization Before.SpendableNano = %d, want %d", authorization.Before.SpendableNano, beforeSpendable)
	}
	if authorization.Before.BalanceNano != beforeAuth.BalanceNano || authorization.Before.ReservedNano != beforeAuth.ReservedNano || authorization.Before.Version != beforeAuth.Version {
		t.Fatalf("authorization Before snapshot = %+v, want account %+v", authorization.Before, beforeAuth)
	}
	if authorization.After.SpendableNano != beforeSpendable-10 {
		t.Fatalf("authorization spendable after = %d, want %d", authorization.After.SpendableNano, beforeSpendable-10)
	}
	if authorization.After.ReservedNano != beforeAuth.ReservedNano+10 {
		t.Fatalf("authorization reserved after = %d, want %d", authorization.After.ReservedNano, beforeAuth.ReservedNano+10)
	}
	if authorization.After.Version != beforeAuth.Version+1 {
		t.Fatalf("authorization version after = %d, want %d", authorization.After.Version, beforeAuth.Version+1)
	}
	if _, err := store.Authorize(ctx, authorizationInput(accountID, "authorization-turn", accountID+"-authorization", 10)); err != nil {
		t.Fatalf("same authorization replay: %v", err)
	}
	account, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Version != authorization.After.Version || account.ReservedNano != authorization.After.ReservedNano {
		t.Fatalf("account after authorization = %+v, want version/reserved from snapshot %+v", account, authorization.After)
	}

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
