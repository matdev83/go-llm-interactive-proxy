package billingstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

func testIndependentCallUsage(t *testing.T, expected []string) billing.CallUsageRecord {
	t.Helper()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return testIndependentCallUsageFor(callID, expected)
}

func testIndependentCallUsageFor(callID billing.BillingCallID, expected []string) billing.CallUsageRecord {
	return billing.CallUsageRecord{
		SchemaVersion: billing.CurrentRecordSchemaVersion,
		CallID:        callID,
		AccountID:     "acct-corr",
		ALegID:        "a-shared",
		SessionID:     "sess-shared",
		StartedAt:     time.Unix(100, 0).UTC(),
		FinishedAt:    time.Unix(101, 0).UTC(),
		Outcome:       billing.TurnOutcomeCompleted,
		CustomerPricingRef: billing.VersionRef{
			ID:      "prices",
			Version: "v1",
		},
		ChargePolicyRef: billing.VersionRef{
			ID:      "policy",
			Version: "v2",
		},
		ExpectedBLegIDs: expected,
	}
}

func TestSQLiteAppendCallUsagePersistsIndependentOfTURAndJournal(t *testing.T) {
	runAppendCallUsageIndependentOfMoneyAndTUR(t, newSQLiteTestStore(t), "call-usage-independent")
}

func TestSQLiteAppendCallUsageReplayAndFingerprintConflict(t *testing.T) {
	runAppendCallUsageReplayAndFingerprintConflict(t, newSQLiteTestStore(t))
}

func TestSQLiteClaimCompleteCallIndependentOfAppendOrder(t *testing.T) {
	runClaimCompleteCallIndependentOfAppendOrder(t, newSQLiteTestStore(t))
}

func TestSQLiteClaimCompleteCallsSkipsIncompleteOldestAndClaimsNewerComplete(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	incompleteID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	completeID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(incompleteID, []string{"b-missing"})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.AppendCallUsage(ctx, testIndependentCallUsageFor(completeID, []string{"b-ready"})); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(completeID, "b-ready")); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimCompleteCalls(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Closure.CallID != completeID {
		t.Fatalf("claimed = %+v, want only complete call %s", claimed, completeID)
	}
}

func TestSQLiteClaimCompleteCallsContinuesPastClaimConflict(t *testing.T) {
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
	first := testIndependentCallUsageFor(firstID, []string{"b-1"})
	first.AccountID = "acct-claim-a"
	second := testIndependentCallUsageFor(secondID, []string{"b-1"})
	second.AccountID = "acct-claim-b"
	for _, call := range []billing.CallUsageRecord{first, second} {
		if err := store.AppendCallUsage(ctx, call); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendCallLegUsage(ctx, testIndependentCallLegFor(call.CallID, "b-1")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	ids := []string{firstID.String(), secondID.String()}
	if _, err := store.ClaimCompleteCall(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.claimCompleteCallsFromIDs(ctx, ids, 2, claimCompleteOpts{WorkerBatch: true, Now: time.Now().UTC()})
	if errors.Is(err, billing.ErrCallClaimConflict) {
		t.Fatalf("ClaimCompleteCalls must skip claim conflicts, got %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Closure.CallID != secondID {
		t.Fatalf("claimed = %+v, want only second complete call %s", claimed, secondID)
	}
}

func TestSQLiteCallUsageALegSessionAreCorrelationNotKey(t *testing.T) {
	runCallUsageALegSessionAreCorrelationNotKey(t, newSQLiteTestStore(t))
}

func TestSQLiteUsageCallRecordsPayloadImmutableClaimMutable(t *testing.T) {
	runUsageCallRecordsPayloadImmutableClaimMutable(t, newSQLiteTestStore(t))
}

func TestAppendCallUsageRejectsUnsealableRecord(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	record := testIndependentCallUsage(t, []string{"b-1"})
	record.FinishedAt = time.Time{}
	err := store.AppendCallUsage(ctx, record)
	if !errors.Is(err, billing.ErrInvalidRecord) {
		t.Fatalf("unsealable append = %v, want ErrInvalidRecord", err)
	}
}

func runAppendCallUsageIndependentOfMoneyAndTUR(t *testing.T, store *DurableStore, accountID string) {
	t.Helper()
	ctx := context.Background()
	journalAccount(t, store, accountID)
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
	var beforeTUR, beforeNestedLUR, beforeIndependentLeg, beforeJournalEntries int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM turn_usage_records`).Scan(ctx, &beforeTUR); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM leg_usage_records`).Scan(ctx, &beforeNestedLUR); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_leg_records`).Scan(ctx, &beforeIndependentLeg); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_entries`).Scan(ctx, &beforeJournalEntries); err != nil {
		t.Fatal(err)
	}

	record := testIndependentCallUsage(t, []string{"b-win", "b-fail"})
	if err := store.AppendCallUsage(ctx, record); err != nil {
		t.Fatalf("AppendCallUsage: %v", err)
	}

	afterAccount, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAccount.BalanceNano != beforeAccount.BalanceNano || afterAccount.ReservedNano != beforeAccount.ReservedNano || afterAccount.Version != beforeAccount.Version {
		t.Fatalf("append mutated account money/version: before=%#v after=%#v", beforeAccount, afterAccount)
	}
	afterJournals, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterJournals) != len(beforeJournals) {
		t.Fatalf("journal rows mutated: before=%d after=%d", len(beforeJournals), len(afterJournals))
	}
	var afterTUR, afterNestedLUR, afterIndependentLeg, afterJournalEntries, afterCall int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM turn_usage_records`).Scan(ctx, &afterTUR); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM leg_usage_records`).Scan(ctx, &afterNestedLUR); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_leg_records`).Scan(ctx, &afterIndependentLeg); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM journal_entries`).Scan(ctx, &afterJournalEntries); err != nil {
		t.Fatal(err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_call_records`).Scan(ctx, &afterCall); err != nil {
		t.Fatal(err)
	}
	if afterTUR != beforeTUR || afterNestedLUR != beforeNestedLUR || afterIndependentLeg != beforeIndependentLeg {
		t.Fatal("call-closure append must not write TUR, nested LUR, or usage_leg_records")
	}
	if afterJournalEntries != beforeJournalEntries {
		t.Fatal("call-closure append must not post journal entries")
	}
	if afterCall != 1 {
		t.Fatalf("usage_call_records rows = %d, want 1", afterCall)
	}
	got, err := store.GetCallUsage(ctx, record.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CallID != record.CallID || got.Key != record.CallID.String() {
		t.Fatalf("loaded record key = %q call = %q", got.Key, got.CallID)
	}
	if strings.Contains(got.Key, "a-shared") || strings.Contains(got.Key, "sess-shared") || strings.Contains(got.Key, "acct-corr") {
		t.Fatal("durable call key must be BillingCallID, not account/A-leg/session")
	}
}

func runAppendCallUsageReplayAndFingerprintConflict(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	record := testIndependentCallUsageFor(callID, []string{"b-2", "b-1"})
	const count = 12
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			errs <- store.AppendCallUsage(context.Background(), record)
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
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &countRows); err != nil {
		t.Fatal(err)
	}
	if countRows != 1 {
		t.Fatalf("usage_call_records rows = %d, want 1", countRows)
	}
	reordered := record
	reordered.ExpectedBLegIDs = []string{"b-1", "b-2"}
	if err := store.AppendCallUsage(ctx, reordered); err != nil {
		t.Fatalf("same-set replay: %v", err)
	}
	conflict := record
	conflict.Outcome = billing.TurnOutcomeFailed
	if err := store.AppendCallUsage(ctx, conflict); !errors.Is(err, billing.ErrReplayConflict) {
		t.Fatalf("conflicting replay = %v, want ErrReplayConflict", err)
	}
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM usage_call_records WHERE call_id = ?`, callID.String()).Scan(ctx, &countRows); err != nil {
		t.Fatal(err)
	}
	if countRows != 1 {
		t.Fatalf("conflict mutated rows = %d, want 1", countRows)
	}
}

func runClaimCompleteCallIndependentOfAppendOrder(t *testing.T, store *DurableStore) {
	t.Helper()
	cases := []struct {
		name  string
		steps []string
	}{
		{name: "legs_first", steps: []string{"leg:b-fail", "leg:b-win", "closure"}},
		{name: "closure_first", steps: []string{"closure", "leg:b-fail", "leg:b-win"}},
		{name: "interleaved", steps: []string{"leg:b-fail", "closure", "leg:b-win"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			callID, err := billing.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			closure := testIndependentCallUsageFor(callID, []string{"b-win", "b-fail"})
			for i, step := range tc.steps {
				switch {
				case step == "closure":
					if err := store.AppendCallUsage(ctx, closure); err != nil {
						t.Fatalf("append closure: %v", err)
					}
				case strings.HasPrefix(step, "leg:"):
					leg := testIndependentCallLegFor(callID, strings.TrimPrefix(step, "leg:"))
					if err := store.AppendCallLegUsage(ctx, leg); err != nil {
						t.Fatalf("append %s: %v", step, err)
					}
				default:
					t.Fatalf("unknown step %q", step)
				}
				got, err := store.ClaimCompleteCall(ctx, callID)
				last := i == len(tc.steps)-1
				if !last {
					if !errors.Is(err, billing.ErrCallIncomplete) {
						t.Fatalf("step %d (%s) claim = %v, want ErrCallIncomplete", i, step, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("complete claim after %s: %v", tc.name, err)
				}
				if got.Closure.CallID != callID || got.Closure.ALegID != "a-shared" || got.Closure.SessionID != "sess-shared" {
					t.Fatalf("claimed closure = %+v", got.Closure)
				}
				if len(got.Legs) != 2 {
					t.Fatalf("claimed legs = %d, want 2", len(got.Legs))
				}
				if _, err := store.ClaimCompleteCall(ctx, callID); !errors.Is(err, billing.ErrCallClaimConflict) {
					t.Fatalf("second complete-call claim = %v, want ErrCallClaimConflict", err)
				}
			}
		})
	}
}

func runCallUsageALegSessionAreCorrelationNotKey(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	firstID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	first := testIndependentCallUsageFor(firstID, []string{"b-1"})
	second := testIndependentCallUsageFor(secondID, []string{"b-2"})
	if err := store.AppendCallUsage(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCallUsage(ctx, second); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := store.GetCallUsage(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := store.GetCallUsage(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if gotFirst.ALegID != gotSecond.ALegID || gotFirst.SessionID != gotSecond.SessionID {
		t.Fatal("fixture must reuse A-leg/session correlation")
	}
	if gotFirst.Key == gotSecond.Key || gotFirst.CallID == gotSecond.CallID {
		t.Fatal("later calls on the same A-leg must be distinct BillingCallID keys")
	}
}

func runUsageCallRecordsPayloadImmutableClaimMutable(t *testing.T, store *DurableStore) {
	t.Helper()
	ctx := context.Background()
	record := testIndependentCallUsage(t, []string{"b-imm"})
	if err := store.AppendCallUsage(ctx, record); err != nil {
		t.Fatal(err)
	}
	key := record.CallID.String()
	if _, err := store.db.NewRaw(`UPDATE usage_call_records SET outcome = 'failed' WHERE usage_call_key = ?`, key).Exec(ctx); err == nil {
		t.Fatal("usage_call_records payload update must be rejected")
	}
	if _, err := store.db.NewRaw(`DELETE FROM usage_call_records WHERE usage_call_key = ?`, key).Exec(ctx); err == nil {
		t.Fatal("usage_call_records delete must be rejected")
	}
	if _, err := store.db.NewRaw(`UPDATE usage_call_records SET claim_status = 'claimed' WHERE usage_call_key = ?`, key).Exec(ctx); err != nil {
		t.Fatalf("claim_status update must be allowed: %v", err)
	}
	var payload string
	if err := store.db.NewRaw(`SELECT payload_json FROM usage_call_records WHERE usage_call_key = ?`, key).Scan(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	var decoded billing.CallUsageRecord
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome != billing.TurnOutcomeCompleted {
		t.Fatalf("payload mutated: %+v", decoded)
	}
}
