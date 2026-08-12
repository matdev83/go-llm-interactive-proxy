package billingstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func testTUR(accountID string) billing.TurnUsageRecord {
	return billing.TurnUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		AccountID:     accountID, TurnID: "tur-turn", ALegID: "tur-a", AuthorizationID: "tur-auth",
		StartedAt: time.Unix(20, 0).UTC(), FinishedAt: time.Unix(21, 0).UTC(), Outcome: billing.TurnOutcomeCompleted,
		Legs: []billing.LegUsageRecord{{
			ALegID: "tur-a", BLegID: "tur-b", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
			StartedAt: time.Unix(20, 0).UTC(), FinishedAt: time.Unix(21, 0).UTC(), Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		}},
	}
}

func TestImmutableBillingRowsRejectUpdateAndDelete(t *testing.T) {
	runImmutableBillingRowsRejectUpdateAndDelete(t, newSQLiteTestStore(t), "immutable-account")
}

func TestAppendUsageRecordConcurrentSameReplayIsIdempotent(t *testing.T) {
	runAppendUsageRecordReplayAndFingerprintConflict(t, newSQLiteTestStore(t), "replay-account")
}

func TestAppendUsageRecordReplayRejectsMismatchedProcessingFingerprint(t *testing.T) {
	runAppendUsageRecordReplayRejectsMismatchedProcessingFingerprint(t, newSQLiteTestStore(t), "proc-fp-account")
}

func runImmutableBillingRowsRejectUpdateAndDelete(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	journalAccount(t, store, accountID)
	if err := store.AppendUsageRecord(context.Background(), testTUR(accountID)); err != nil {
		t.Fatal(err)
	}
	txID := accountID + "-tx"
	journal := journalInput(txID, accountID+"-source", 10)
	journal.AccountID = accountID
	posted, err := store.postJournalTransaction(context.Background(), journal)
	if err != nil {
		t.Fatal(err)
	}
	turKey, err := billing.TURKey(accountID, "tur-turn")
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		statement string
		args      []any
	}{
		{`UPDATE turn_usage_records SET outcome = 'failed' WHERE tur_key = ?`, []any{turKey}},
		{`DELETE FROM turn_usage_records WHERE tur_key = ?`, []any{turKey}},
		{`UPDATE journal_transactions SET source_key = 'changed' WHERE transaction_id = ?`, []any{txID}},
		{`DELETE FROM journal_transactions WHERE transaction_id = ?`, []any{txID}},
		{`UPDATE journal_entries SET amount_nano = 11 WHERE transaction_id = ? AND ordinal = 0`, []any{txID}},
		{`DELETE FROM journal_entries WHERE transaction_id = ? AND ordinal = 0`, []any{txID}},
	} {
		if _, err := store.db.NewRaw(probe.statement, probe.args...).Exec(context.Background()); err == nil {
			t.Fatalf("%s unexpectedly succeeded", probe.statement)
		}
	}
	if posted.ID != txID {
		t.Fatalf("posted id = %q", posted.ID)
	}
}

func runAppendUsageRecordReplayAndFingerprintConflict(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	journalAccount(t, store, accountID)
	record := testTUR(accountID)
	const count = 12
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			errs <- store.AppendUsageRecord(context.Background(), record)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay: %v", err)
		}
	}
	var countRows int
	turKey, err := billing.TURKey(accountID, "tur-turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM turn_usage_records WHERE tur_key = ?`, turKey).Scan(context.Background(), &countRows); err != nil {
		t.Fatal(err)
	}
	if countRows != 1 {
		t.Fatalf("TUR rows = %d, want 1", countRows)
	}
	conflict := record
	conflict.Legs[0].ModelID = "different-model"
	if err := store.AppendUsageRecord(context.Background(), conflict); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("conflicting replay = %v, want ErrReplayConflict", err)
	}
}

func runAppendUsageRecordReplayRejectsMismatchedProcessingFingerprint(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	journalAccount(t, store, accountID)
	record := testTUR(accountID)
	ctx := context.Background()
	if err := store.AppendUsageRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.NewRaw(`UPDATE usage_record_processing SET tur_fingerprint = ? WHERE tur_key = ?`, "corrupted-fingerprint", sealed.Key).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsageRecord(ctx, record); !errors.Is(err, billing.ErrProcessingConflict) {
		t.Fatalf("mismatched processing fingerprint = %v, want ErrProcessingConflict", err)
	}
	state, err := store.GetProcessing(ctx, sealed.Key)
	if err != nil {
		t.Fatal(err)
	}
	if state.TURFingerprint != "corrupted-fingerprint" {
		t.Fatalf("processing fingerprint mutated on conflict = %q", state.TURFingerprint)
	}
}
