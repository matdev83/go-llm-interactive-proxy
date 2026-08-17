package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func testIndependentCallLeg(t *testing.T, bLegID string) billing.CallLegUsageRecord {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return testIndependentCallLegFor(callID, bLegID)
}

func testIndependentCallLegFor(callID billing.BillingCallID, bLegID string) billing.CallLegUsageRecord {
	return billing.CallLegUsageRecord{
		CallID:     callID,
		ALegID:     "a-shared",
		BLegID:     bLegID,
		AttemptSeq: 1,
		BackendID:  "backend-a",
		ProviderID: "provider-a",
		ModelID:    "model-a",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(100, 500000000).UTC(),
		Outcome:    billing.LegOutcomeWinner,
		Surfaced:   billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{
			InputTokens:  billing.Quantity{Value: 7, Present: true},
			OutputTokens: billing.Quantity{Value: 3, Present: true},
			Cost:         billing.MoneyEvidence{NanoUnits: 11, Currency: "USD", Present: true},
			Source:       billing.EvidenceSourceProviderReported,
			Authority:    billing.EvidenceAuthorityAuthoritative,
			DedupeKey:    "provider-charge-1",
		},
		OperatorRateRef: billing.VersionRef{ID: "operator-rates", Version: "v4"},
	}
}

func TestSQLiteAppendCallLegUsagePersistsIndependentOfTURAndJournal(t *testing.T) {
	runAppendCallLegUsageIndependentOfMoneyAndTUR(t, newSQLiteTestStore(t), "call-leg-independent")
}

func TestSQLiteAppendCallLegUsageReplayAndFingerprintConflict(t *testing.T) {
	runAppendCallLegUsageReplayAndFingerprintConflict(t, newSQLiteTestStore(t))
}

func TestSQLiteAppendCallLegUsageRejectedNeverStartedEvidenceUnavailable(t *testing.T) {
	runAppendCallLegUsageRejectedNeverStartedEvidenceUnavailable(t, newSQLiteTestStore(t))
}

func TestSQLiteAppendCallLegUsagePreservesQuantityAndCostPresence(t *testing.T) {
	runAppendCallLegUsagePreservesQuantityAndCostPresence(t, newSQLiteTestStore(t))
}

func TestSQLiteAppendCallLegUsageTrimsBLegIDConsistently(t *testing.T) {
	runAppendCallLegUsageTrimsBLegIDConsistently(t, newSQLiteTestStore(t))
}

func TestSQLiteAppendCallLegUsagePayloadExcludesPromptsAndSecrets(t *testing.T) {
	runAppendCallLegUsagePayloadExcludesPromptsAndSecrets(t, newSQLiteTestStore(t))
}

func TestSQLiteUsageLegRecordsAreImmutable(t *testing.T) {
	runUsageLegRecordsAreImmutable(t, newSQLiteTestStore(t))
}

func TestAppendCallLegUsageRejectsUnsealableRecord(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	record := testIndependentCallLeg(t, "b-1")
	record.FinishedAt = time.Time{}
	err := store.AppendCallLegUsage(ctx, record)
	if !errors.Is(err, billing.ErrInvalidRecord) {
		t.Fatalf("unsealable append = %v, want ErrInvalidRecord", err)
	}
}

func TestSQLiteAppendCallLegUsageDoesNotRequireAccount(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	record := testIndependentCallLeg(t, "b-orphan")
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("independent append must not require a billing account: %v", err)
	}
}

func runAppendCallLegUsageIndependentOfMoneyAndTUR(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	journalAccount(t, store, accountID)
	// 8.9/8.10: usage_leg_records is the durable Bun spool. Simultaneous total
	// replica loss before any append succeeds is outside the at-least-once
	// guarantee; this path does not synthesize exactly-once delivery from RAM.
	journal := journalInput(accountID+"-tx", accountID+"-source", 10)
	journal.AccountID = accountID
	if _, err := store.postJournalTransaction(ctx, journal); err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeJournalEntries int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_entries`).Scan(ctx, &beforeJournalEntries); err != nil {
		t.Fatal(err)
	}

	record := testIndependentCallLeg(t, "b-win")
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("AppendCallLegUsage: %v", err)
	}

	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAccount.BalanceNano != beforeAccount.BalanceNano {
		t.Fatalf("append mutated account money/version: before=%#v after=%#v", beforeAccount, afterAccount)
	}
	afterJournals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterJournals) != len(beforeJournals) {
		t.Fatalf("journal rows mutated: before=%d after=%d", len(beforeJournals), len(afterJournals))
	}
	var afterJournalEntries, afterIndependent int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_entries`).Scan(ctx, &afterJournalEntries); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_leg_records`).Scan(ctx, &afterIndependent); err != nil {
		t.Fatal(err)
	}
	if afterJournalEntries != beforeJournalEntries {
		t.Fatal("independent B-leg append must not post journal entries")
	}
	if afterIndependent != 1 {
		t.Fatalf("usage_leg_records rows = %d, want 1", afterIndependent)
	}
	got, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, record.CallID, "b-win"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CallID != record.CallID || got.BLegID != "b-win" || got.Evidence.InputTokens.Value != 7 {
		t.Fatalf("loaded record = %+v", got)
	}
}

func runAppendCallLegUsageReplayAndFingerprintConflict(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	record := testIndependentCallLegFor(callID, "b-1")
	const count = 12
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			errs <- store.AppendCallLegUsage(context.Background(), record)
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
	key := mustCallLegKey(t, callID, "b-1")
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &countRows); err != nil {
		t.Fatal(err)
	}
	if countRows != 1 {
		t.Fatalf("usage_leg_records rows = %d, want 1", countRows)
	}
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	conflict := record
	conflict.ModelID = "different-model"
	if err := store.AppendCallLegUsage(ctx, conflict); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("conflicting replay = %v, want ErrReplayConflict", err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &countRows); err != nil {
		t.Fatal(err)
	}
	if countRows != 1 {
		t.Fatalf("conflict mutated rows = %d, want 1", countRows)
	}
}

func runAppendCallLegUsageRejectedNeverStartedEvidenceUnavailable(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		bLegID   string
		outcome  billing.LegOutcome
		surfaced billing.SurfacedState
	}{
		{name: "rejected", bLegID: "b-rejected", outcome: billing.LegOutcomeRejected, surfaced: billing.SurfacedNo},
		{name: "never_started", bLegID: "b-never", outcome: billing.LegOutcomeNeverStarted, surfaced: billing.SurfacedNo},
		{name: "evidence_unavailable", bLegID: "b-no-ev", outcome: billing.LegOutcomeFailed, surfaced: billing.SurfacedNo},
	}
	for seqIndex, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := testIndependentCallLegFor(callID, tc.bLegID)
			src.AttemptSeq = seqIndex + 1
			src.Outcome = tc.outcome
			src.Surfaced = tc.surfaced
			src.Evidence = billing.FinalBillingEvidence{
				Source:    billing.EvidenceSourceUnavailable,
				Authority: billing.EvidenceAuthorityUnavailable,
			}
			if tc.outcome == billing.LegOutcomeNeverStarted {
				src.FinishedAt = src.StartedAt
			}
			if err := store.AppendCallLegUsage(ctx, src); err != nil {
				t.Fatalf("append %s: %v", tc.name, err)
			}
			got, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, tc.bLegID))
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.outcome)
			}
			if got.Evidence.Source != billing.EvidenceSourceUnavailable || got.Evidence.Authority != billing.EvidenceAuthorityUnavailable {
				t.Fatalf("evidence unavailable not stored: %+v", got.Evidence)
			}
		})
	}
}

func runAppendCallLegUsagePreservesQuantityAndCostPresence(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	absent := testIndependentCallLegFor(callID, "b-absent")
	absent.AttemptSeq = 1
	absent.Evidence.InputTokens = billing.Quantity{}
	absent.Evidence.Cost = billing.MoneyEvidence{}
	zero := testIndependentCallLegFor(callID, "b-zero")
	zero.AttemptSeq = 2
	zero.Evidence.InputTokens = billing.Quantity{Present: true}
	zero.Evidence.Cost = billing.MoneyEvidence{Currency: "USD", Present: true}
	if err := store.AppendCallLegUsage(ctx, absent); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, zero); err != nil {
		t.Fatal(err)
	}
	gotAbsent, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-absent"))
	if err != nil {
		t.Fatal(err)
	}
	gotZero, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-zero"))
	if err != nil {
		t.Fatal(err)
	}
	if gotAbsent.Evidence.InputTokens.Present || gotAbsent.Evidence.Cost.Present {
		t.Fatalf("absent presence mutated: %+v", gotAbsent.Evidence)
	}
	if !gotZero.Evidence.InputTokens.Present || gotZero.Evidence.InputTokens.Value != 0 {
		t.Fatalf("explicit zero input not preserved: %+v", gotZero.Evidence.InputTokens)
	}
	if !gotZero.Evidence.Cost.Present || gotZero.Evidence.Cost.NanoUnits != 0 {
		t.Fatalf("explicit zero cost not preserved: %+v", gotZero.Evidence.Cost)
	}
	if gotAbsent.Fingerprint == gotZero.Fingerprint {
		t.Fatal("absent vs explicit-zero fingerprints must differ after persist")
	}
	conflict := absent
	conflict.Evidence.InputTokens = billing.Quantity{Present: true}
	if err := store.AppendCallLegUsage(ctx, conflict); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("presence conflict = %v, want ErrReplayConflict", err)
	}
}

func runAppendCallLegUsageTrimsBLegIDConsistently(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	padded := testIndependentCallLegFor(callID, "  b-trim  ")
	if err := store.AppendCallLegUsage(ctx, padded); err != nil {
		t.Fatal(err)
	}
	key := mustCallLegKey(t, callID, "b-trim")
	var storedBLeg string
	if err := store.db.NewRaw(`SELECT b_leg_id FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &storedBLeg); err != nil {
		t.Fatal(err)
	}
	if storedBLeg != "b-trim" {
		t.Fatalf("stored b_leg_id = %q, want trimmed b-trim", storedBLeg)
	}
	trimmed := testIndependentCallLegFor(callID, "b-trim")
	if err := store.AppendCallLegUsage(ctx, trimmed); err != nil {
		t.Fatalf("trimmed replay of padded insert: %v", err)
	}
}

func runAppendCallLegUsagePayloadExcludesPromptsAndSecrets(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	record := testIndependentCallLeg(t, "b-safe")
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatal(err)
	}
	var payload string
	key := mustCallLegKey(t, record.CallID, "b-safe")
	if err := store.db.NewRaw(`SELECT payload_json FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(payload)
	for _, banned := range []string{
		"prompt", "completion", "secret", "authorization_header",
		"authorizationheader", "api_key", "apikey", "bearer ",
	} {
		if strings.Contains(lower, banned) {
			t.Fatalf("stored payload must not contain %q: %s", banned, payload)
		}
	}
	var decoded billing.CallLegUsageRecord
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Evidence.DedupeKey != "provider-charge-1" {
		t.Fatalf("payload lost billing-safe evidence: %+v", decoded.Evidence)
	}
}

func runUsageLegRecordsAreImmutable(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	record := testIndependentCallLeg(t, "b-imm")
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatal(err)
	}
	key := mustCallLegKey(t, record.CallID, "b-imm")
	if _, err := store.db.NewRaw(`UPDATE usage_leg_records SET outcome = 'failed' WHERE usage_leg_key = ?`, key).Exec(ctx); err == nil {
		t.Fatal("usage_leg_records update must be rejected")
	}
	if _, err := store.db.NewRaw(`DELETE FROM usage_leg_records WHERE usage_leg_key = ?`, key).Exec(ctx); err == nil {
		t.Fatal("usage_leg_records delete must be rejected")
	}
}

func mustCallLegKey(t *testing.T, callID billing.BillingCallID, bLegID string) string {
	t.Helper()
	key, err := billing.CallLegUsageKey(callID, bLegID)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSQLiteAppendCallLegUsagePersistsAttemptSequence(t *testing.T) {
	runAppendCallLegUsagePersistsAttemptSequence(t, newSQLiteTestStore(t))
}

func TestSQLiteAppendCallLegUsageRejectsDuplicateAttemptSequenceWithinCall(t *testing.T) {
	runAppendCallLegUsageRejectsDuplicateAttemptSequenceWithinCall(t, newSQLiteTestStore(t))
}

func TestSQLiteAppendCallLegUsageReplayConflictOnChangedSequence(t *testing.T) {
	runAppendCallLegUsageReplayConflictOnChangedSequence(t, newSQLiteTestStore(t))
}

func TestSQLiteLegacyNullAttemptSequenceRowsRemainReadable(t *testing.T) {
	runLegacyNullAttemptSequenceRowsRemainReadable(t, newSQLiteTestStore(t))
}

func runAppendCallLegUsagePersistsAttemptSequence(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	record := testIndependentCallLegFor(callID, "b-1")
	record.AttemptSeq = 3
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("AppendCallLegUsage: %v", err)
	}
	var attemptSeq sql.NullInt64
	key := mustCallLegKey(t, callID, "b-1")
	if err := store.db.NewRaw(`SELECT attempt_seq FROM usage_leg_records WHERE usage_leg_key = ?`, key).Scan(ctx, &attemptSeq); err != nil {
		t.Fatal(err)
	}
	if !attemptSeq.Valid || attemptSeq.Int64 != 3 {
		t.Fatalf("persisted attempt_seq = %+v, want 3", attemptSeq)
	}
	got, err := store.GetCallLegUsage(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptSeq != 3 {
		t.Fatalf("restored AttemptSeq = %d, want 3 (no inference)", got.AttemptSeq)
	}
}

func runAppendCallLegUsageRejectsDuplicateAttemptSequenceWithinCall(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	first := testIndependentCallLegFor(callID, "b-first")
	first.AttemptSeq = 2
	if err := store.AppendCallLegUsage(ctx, first); err != nil {
		t.Fatalf("first append: %v", err)
	}
	second := testIndependentCallLegFor(callID, "b-second")
	second.AttemptSeq = 2 // same positive sequence in the same call -> conflict
	if err := store.AppendCallLegUsage(ctx, second); !errors.Is(err, ErrLegAttemptSequenceConflict) {
		t.Fatalf("duplicate attempt_seq append = %v, want ErrLegAttemptSequenceConflict", err)
	}
	var rows int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_leg_records WHERE call_id = ?`, callID.String()).Scan(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("conflicting rows = %d, want 1", rows)
	}
	// A different positive sequence in the same call is fine.
	third := testIndependentCallLegFor(callID, "b-third")
	third.AttemptSeq = 3
	if err := store.AppendCallLegUsage(ctx, third); err != nil {
		t.Fatalf("distinct sequence append: %v", err)
	}
}

func runAppendCallLegUsageReplayConflictOnChangedSequence(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	record := testIndependentCallLegFor(callID, "b-1")
	record.AttemptSeq = 1
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.AppendCallLegUsage(ctx, record); err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	changed := record
	changed.AttemptSeq = 2
	if err := store.AppendCallLegUsage(ctx, changed); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("same-key changed-sequence replay = %v, want ErrReplayConflict", err)
	}
	got, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptSeq != 1 {
		t.Fatalf("AttemptSeq after conflict = %d, want original 1", got.AttemptSeq)
	}
}

func runLegacyNullAttemptSequenceRowsRemainReadable(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	for _, bLegID := range []string{"b-legacy-1", "b-legacy-2"} {
		// Simulate a pre-fix row: v1-sealed payload (no sequence) persisted
		// with attempt_seq NULL.
		legacySrc := testIndependentCallLegFor(callID, bLegID)
		legacySrc.AttemptSeq = 0
		legacy, sealErr := legacySrc.Seal()
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if legacy.AttemptSeq != 0 {
			t.Fatalf("legacy fixture AttemptSeq = %d, want 0", legacy.AttemptSeq)
		}
		payload, marshalErr := json.Marshal(legacy)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		sealedAt := time.Now().UTC()
		if _, err := store.db.NewRaw(`INSERT INTO usage_leg_records( usage_leg_key, fingerprint, call_id, a_leg_id, b_leg_id, backend_id, provider_id, model_id, started_at, finished_at, outcome, surfaced, payload_json, sealed_at ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, legacy.Key, legacy.Fingerprint, legacy.CallID.String(), legacy.ALegID, legacy.BLegID, legacy.BackendID, legacy.ProviderID, legacy.ModelID, legacy.StartedAt, legacy.FinishedAt, string(legacy.Outcome), string(legacy.Surfaced), string(payload), sealedAt).Exec(ctx); err != nil {
			t.Fatalf("legacy insert: %v", err)
		}
		// NULL sequences may coexist with no guessed values.
		var attemptSeq sql.NullString
		if err := store.db.NewRaw(`SELECT attempt_seq FROM usage_leg_records WHERE usage_leg_key = ?`, legacy.Key).Scan(ctx, &attemptSeq); err != nil {
			t.Fatal(err)
		}
		if attemptSeq.Valid {
			t.Fatalf("legacy attempt_seq = %q, want NULL", attemptSeq.String)
		}
		got, err := store.GetCallLegUsage(ctx, legacy.Key)
		if err != nil {
			t.Fatalf("legacy row must remain readable under the old contract: %v", err)
		}
		if got.AttemptSeq != 0 {
			t.Fatalf("legacy AttemptSeq = %d, want 0 (unknown)", got.AttemptSeq)
		}
	}
	legs, err := store.ListCallLegUsage(ctx, callID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 2 {
		t.Fatalf("legacy legs = %d, want 2", len(legs))
	}
	// A new corrected leg (positive sequence) can join the same call.
	newer := testIndependentCallLegFor(callID, "b-new")
	newer.AttemptSeq = 1
	if err := store.AppendCallLegUsage(ctx, newer); err != nil {
		t.Fatalf("new leg alongside legacy rows: %v", err)
	}
	got, err := store.GetCallLegUsage(ctx, mustCallLegKey(t, callID, "b-new"))
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptSeq != 1 {
		t.Fatalf("new leg AttemptSeq = %d, want 1", got.AttemptSeq)
	}
}
