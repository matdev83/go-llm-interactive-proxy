package billingstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func journalAccount(t *testing.T, store *DurableStore, id string) {
	t.Helper()
	if err := store.CreateAccount(context.Background(), billing.Account{ID: id, Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, BalanceNano: 100000, Version: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteCreateAccountMapsUniqueConflict(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := billing.Account{ID: "dup-account", Currency: "USD", Mode: billing.AccountPrepaid, State: billing.AccountReady, BalanceNano: 1, Version: 1}
	if err := store.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	err := store.CreateAccount(ctx, account)
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("duplicate create = %v, want identity conflict", err)
	}
	if !errors.Is(err, billing.ErrAccountConflict) {
		t.Fatalf("duplicate create = %v, want billing.ErrAccountConflict", err)
	}
}

func TestSQLiteCreateAccountRejectsOpeningReservation(t *testing.T) {
	store := newSQLiteTestStore(t)
	err := store.CreateAccount(context.Background(), billing.Account{
		ID: "reserved-open", Currency: "USD", Mode: billing.AccountPrepaid,
		State: billing.AccountReady, BalanceNano: 100, ReservedNano: 25, Version: 1,
	})
	if !errors.Is(err, billing.ErrAccountInvalid) {
		t.Fatalf("opening reservation = %v, want ErrAccountInvalid", err)
	}
	if _, err := store.GetAccount(context.Background(), "reserved-open"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("rejected opening must not persist: %v", err)
	}
}

func journalInput(id, source string, amount int64) billing.JournalTransaction {
	return billing.JournalTransaction{
		ID: id, Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: source,
		AccountID: "acct-journal", TurnID: "turn-1", ALegID: "a-1",
		Entries: []billing.JournalEntry{
			{LedgerAccount: "customer", Side: billing.JournalDebit, Amount: billing.Money{Nano: amount, Currency: "USD"}},
			{LedgerAccount: "revenue", Side: billing.JournalCredit, Amount: billing.Money{Nano: amount, Currency: "USD"}},
		},
	}
}

func TestPostJournalTransactionReplayComparesSemanticFingerprint(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	first, err := store.postJournalTransaction(context.Background(), journalInput("tx-1", "usage-1", 10))
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	if first.AccountSequence != 1 {
		t.Fatalf("first sequence = %d, want 1", first.AccountSequence)
	}
	replay, err := store.postJournalTransaction(context.Background(), journalInput("different-id", "usage-1", 10))
	if err != nil {
		t.Fatalf("same replay: %v", err)
	}
	if replay.ID != first.ID || replay.AccountSequence != first.AccountSequence {
		t.Fatalf("replay = %#v, want original identity/sequence %#v", replay, first)
	}
	_, err = store.postJournalTransaction(context.Background(), journalInput("tx-conflict", "usage-1", 11))
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("conflicting replay = %v, want ErrIdentityConflict", err)
	}
	rows, err := store.JournalTransactions(context.Background(), "acct-journal")
	if err != nil || len(rows) != 1 {
		t.Fatalf("journal rows = %d, err=%v; want one", len(rows), err)
	}
}

func TestPostJournalTransactionCorrectionLinksAreAuditable(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	original, err := store.postJournalTransaction(context.Background(), journalInput("tx-original", "usage-original", 10))
	if err != nil {
		t.Fatalf("original post: %v", err)
	}
	reversalInput := journalInput("tx-reversal", "usage-reversal", 10)
	reversalInput.ReversalOf = original.ID
	reversalInput.Entries[0].Side = billing.JournalCredit
	reversalInput.Entries[1].Side = billing.JournalDebit
	reversal, err := store.postJournalTransaction(context.Background(), reversalInput)
	if err != nil {
		t.Fatalf("reversal post: %v", err)
	}
	if reversal.CorrectionGroupID != original.ID || reversal.ReversalOf != original.ID {
		t.Fatalf("reversal linkage = %#v, want group/original %q", reversal, original.ID)
	}
	replacementInput := journalInput("tx-replacement", "usage-replacement", 9)
	replacementInput.CorrectsTransactionID = original.ID
	replacement, err := store.postJournalTransaction(context.Background(), replacementInput)
	if err != nil {
		t.Fatalf("replacement post: %v", err)
	}
	if replacement.CorrectionGroupID != reversal.CorrectionGroupID || replacement.CorrectsTransactionID != original.ID {
		t.Fatalf("replacement linkage = %#v, want group %q", replacement, reversal.CorrectionGroupID)
	}
	unreversed, err := store.postJournalTransaction(context.Background(), journalInput("tx-unreversed", "usage-unreversed", 4))
	if err != nil {
		t.Fatalf("unreversed original: %v", err)
	}
	orphanReplacement := journalInput("tx-orphan-replacement", "usage-orphan-replacement", 8)
	orphanReplacement.CorrectsTransactionID = unreversed.ID
	if _, err := store.postJournalTransaction(context.Background(), orphanReplacement); !errors.Is(err, ErrCorrectionInvalid) {
		t.Fatalf("replacement without prior reversal = %v, want ErrCorrectionInvalid", err)
	}
	bothLinks := journalInput("tx-both", "usage-both", 10)
	bothLinks.ReversalOf = original.ID
	bothLinks.CorrectsTransactionID = "other-target"
	if _, err := store.postJournalTransaction(context.Background(), bothLinks); !errors.Is(err, ErrCorrectionInvalid) {
		t.Fatalf("different correction targets = %v, want ErrCorrectionInvalid", err)
	}
	wrongScope := journalInput("tx-wrong", "usage-wrong", 10)
	wrongScope.AccountID = "other-account"
	wrongScope.ReversalOf = original.ID
	if _, err := store.postJournalTransaction(context.Background(), wrongScope); !errors.Is(err, ErrCorrectionInvalid) {
		t.Fatalf("wrong-scope correction = %v, want ErrCorrectionInvalid", err)
	}
}

func TestPostJournalTransactionAllocatesAccountSequenceConcurrently(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	const count = 20
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := journalInput("tx-concurrent-"+itoa(i), "usage-concurrent-"+itoa(i), 1)
			_, err := store.postJournalTransaction(context.Background(), input)
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
	rows, err := store.JournalTransactions(context.Background(), "acct-journal")
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

func TestPostJournalTransactionConcurrentSameSourceKeyIsIdempotent(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	const workers = 16
	results := make(chan billing.JournalTransaction, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct caller-supplied IDs; durable identity is SourceKey.
			input := journalInput("tx-same-source-"+itoa(i), "usage-same-source", 7)
			posted, err := store.postJournalTransaction(context.Background(), input)
			if err != nil {
				errs <- err
				return
			}
			results <- posted
		}(i)
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("concurrent same-SourceKey post: %v", err)
	}
	var first billing.JournalTransaction
	count := 0
	for posted := range results {
		if count == 0 {
			first = posted
		} else if posted.ID != first.ID || posted.AccountSequence != first.AccountSequence || posted.SemanticFingerprint != first.SemanticFingerprint {
			t.Fatalf("idempotent results diverged: %#v vs %#v", posted, first)
		}
		count++
	}
	if count != workers {
		t.Fatalf("result count = %d, want %d", count, workers)
	}
	rows, err := store.JournalTransactions(context.Background(), "acct-journal")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AccountSequence != 1 {
		t.Fatalf("journal rows = %#v, want exactly one sequence-1 row", rows)
	}
	if !strings.HasPrefix(rows[0].SemanticFingerprint, billing.JournalFingerprintPrefix) {
		t.Fatalf("stored fingerprint %q missing version prefix", rows[0].SemanticFingerprint)
	}

	_, err = store.postJournalTransaction(context.Background(), journalInput("tx-conflict", "usage-same-source", 8))
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("conflicting fingerprint = %v, want ErrIdentityConflict", err)
	}
	rows, err = store.JournalTransactions(context.Background(), "acct-journal")
	if err != nil || len(rows) != 1 {
		t.Fatalf("after conflict rows = %d err=%v; want one original", len(rows), err)
	}
}

func TestPostJournalTransactionRejectsSecondReversalOfSameOriginal(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	original, err := store.postJournalTransaction(context.Background(), journalInput("tx-original", "usage-original", 10))
	if err != nil {
		t.Fatalf("original post: %v", err)
	}
	reversalInput := journalInput("tx-reversal", "usage-reversal", 10)
	reversalInput.ReversalOf = original.ID
	reversalInput.Entries[0].Side = billing.JournalCredit
	reversalInput.Entries[1].Side = billing.JournalDebit
	if _, err := store.postJournalTransaction(context.Background(), reversalInput); err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	// Idempotent replay of the same reversal SourceKey must still succeed.
	if _, err := store.postJournalTransaction(context.Background(), reversalInput); err != nil {
		t.Fatalf("reversal replay: %v", err)
	}
	second := journalInput("tx-reversal-2", "usage-reversal-2", 10)
	second.ReversalOf = original.ID
	second.Entries[0].Side = billing.JournalCredit
	second.Entries[1].Side = billing.JournalDebit
	if _, err := store.postJournalTransaction(context.Background(), second); !errors.Is(err, ErrCorrectionInvalid) {
		t.Fatalf("second reversal = %v, want ErrCorrectionInvalid", err)
	}
	rows, err := store.JournalTransactions(context.Background(), "acct-journal")
	if err != nil || len(rows) != 2 {
		t.Fatalf("journal rows = %d err=%v; want original+one reversal", len(rows), err)
	}
}

func TestPostJournalTransactionConcurrentDistinctReversalsRejectAllButOne(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	original, err := store.postJournalTransaction(context.Background(), journalInput("tx-original-race", "usage-original-race", 10))
	if err != nil {
		t.Fatalf("original post: %v", err)
	}
	const workers = 32
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := journalInput("tx-reversal-race-"+itoa(i), "usage-reversal-race-"+itoa(i), 10)
			input.ReversalOf = original.ID
			input.Entries[0].Side = billing.JournalCredit
			input.Entries[1].Side = billing.JournalDebit
			_, err := store.postJournalTransaction(context.Background(), input)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	rejected := 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrCorrectionInvalid):
			rejected++
		default:
			t.Fatalf("unexpected concurrent reversal error: %v", err)
		}
	}
	if accepted != 1 || rejected != workers-1 {
		t.Fatalf("accepted=%d rejected=%d want 1/%d", accepted, rejected, workers-1)
	}
	rows, err := store.JournalTransactions(context.Background(), "acct-journal")
	if err != nil || len(rows) != 2 {
		t.Fatalf("journal rows = %d err=%v; want original+one reversal", len(rows), err)
	}
	reversals := 0
	for _, row := range rows {
		if row.ReversalOf == original.ID {
			reversals++
		}
	}
	if reversals != 1 {
		t.Fatalf("reversal count = %d, want 1", reversals)
	}
}

func TestPostJournalTransactionRollbackLeavesNoPartialRows(t *testing.T) {
	store := newSQLiteTestStore(t)
	journalAccount(t, store, "acct-journal")
	// Occupy the transaction primary key so the journal header insert fails after
	// the account lock is held; the attempt must roll back with no entries left.
	if _, err := store.db.NewRaw(`
INSERT INTO journal_transactions(
 transaction_id, account_id, book, currency, source_key, semantic_fingerprint,
 turn_id, a_leg_id, b_leg_id, account_sequence, reversal_of, corrects_transaction_id,
 correction_group_id, operation_kind, balance_before_nano, balance_after_nano,
 reserved_before_nano, reserved_after_nano, spendable_before_nano, spendable_after_nano,
 credit_floor_nano, credit_limit_nano, mode, snapshot_version_before, snapshot_version_after, recorded_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, "tx-occupied", "acct-journal", "financial", "USD", "occupied-source", "seed",
		"", "", "", 1, "", "", "", "", 0, 0, 0, 0, 0, 0, 0, 0, "prepaid", 0, 0, "2020-01-01T00:00:00Z").Exec(context.Background()); err != nil {
		t.Fatalf("seed occupied id: %v", err)
	}
	input := journalInput("tx-occupied", "fresh-source", 3)
	if _, err := store.postJournalTransaction(context.Background(), input); err == nil {
		t.Fatal("expected primary-key failure")
	}
	var entryCount int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_entries WHERE transaction_id = ?`, "tx-occupied").Scan(context.Background(), &entryCount); err != nil {
		t.Fatal(err)
	}
	if entryCount != 0 {
		t.Fatalf("partial journal_entries = %d, want 0 after rollback", entryCount)
	}
	var sourceCount int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_transactions WHERE source_key = ?`, "fresh-source").Scan(context.Background(), &sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 0 {
		t.Fatalf("rolled-back source_key still present (%d rows)", sourceCount)
	}
}

func itoa(v int) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%10]
		v /= 10
	}
	return string(buf[i:])
}
