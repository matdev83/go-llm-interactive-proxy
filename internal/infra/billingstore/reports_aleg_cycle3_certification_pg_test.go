//go:build integration

package billingstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/stretchr/testify/require"
)

// Cycle 3 C3R3 direct-PostgreSQL equivalence: one integrated test proving
// the same essential end-to-end contracts as the SQLite Cycle 3 suite on
// live PostgreSQL. Shared fixtures and assertions are reused from the
// untagged companion files (same package under the integration tag); only
// content hashing has a PostgreSQL variant (row_to_json instead of
// sqlite_master/rowid). No production file is touched.
//
// Coverage: (A) terminal/resume lifecycle through billing.TerminalUsageSink
// with old-token continuation and fresh-projection exactly-once proof;
// (B) writer-silence idle/retirement with full-content hashes over all 13
// report-read tables including billing_economic_revision_work_state;
// (C) cross-plane late corrections with production writer replay/stale/
// loser fencing and exact totals. Adversarial/poison coverage stays in the
// dedicated PG suites executed alongside (D).

// cycle3PGSnapshotHashes captures canonical SHA-256 contents for every
// billing table on PostgreSQL, so a same-count UPDATE is detectable. Rows
// dump as row_to_json text in sorted order, making the hash deterministic
// for identical contents. Table existence is verified first, so the
// allowlist cannot silently skip a table.
func cycle3PGSnapshotHashes(t *testing.T, store *DurableStore) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := make(map[string]string, len(cycle3BillingTables()))
	for _, table := range cycle3BillingTables() {
		var name string
		require.NoError(t, store.db.NewRaw(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ? LIMIT 1`, table).Scan(ctx, &name))
		require.Equal(t, table, name, "billing table %q must exist: hash proof cannot skip it", table)
		var rows []string
		require.NoError(t, store.db.NewRaw(`SELECT row_to_json(t)::text AS j FROM `+table+` AS t ORDER BY 1`).Scan(ctx, &rows))
		sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
		out[table] = hex.EncodeToString(sum[:])
	}
	return out
}

func TestPostgresCycle3FullEquivalenceCertification(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 8)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const accountID, aLegID = "aleg-pg-c3r3", "a-leg-pg-c3r3"
	seedALegCustomerAccount(t, store, accountID)
	var sink billing.TerminalUsageSink = store

	// Phase 1 (A): call A closes through the production terminal handoff
	// (settled 20, leg b-a); call B settles as pass-through (posted 60)
	// with a positive provider-cost revision (adjustment -20).
	callA := cycle3TerminalSettledCall(t, ctx, sink, store, accountID, aLegID, "b-a", 20)
	callB, _ := seedALegPassThroughCall(t, store, accountID, aLegID, 60)
	up := phase10ProviderCost(2, 80, "USD")
	upSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callB, up)
	require.NoError(t, err)
	applyALegPassThroughRevision(t, store, accountID, callB, up)

	preAppend := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, 2, preAppend.CallCount)
	require.Equal(t, int64(20), findALegCall(t, preAppend, callA).CustomerCharge.Nano)
	callADTOBefore := cycle3CallDTOBytes(t, preAppend, callA)

	// Real first-page token over the two settled calls.
	page1 := queryALegCustomerReport(t, store, accountID, aLegID, 1, "")
	token := page1.NextCursor
	require.NotEmpty(t, token, "a truncated first page must mint a real page token")
	var page1Calls, page1Legs []string
	for _, row := range page1.Calls {
		page1Calls = append(page1Calls, row.CallID.String())
	}
	for _, c := range page1.Contributions {
		page1Legs = append(page1Legs, c.Call.CallID.String()+"\x00"+c.BLegID)
	}

	// Phase 2 (B): the same A-leg resumes through normal allocation
	// (distinct BillingCallID, new B-legs) with provider multi-child
	// economics, plus a pass-through negative correction on call B.
	callC := seedC2BCall(t, store, accountID, aLegID)
	require.NotEqual(t, callA.String(), callC.String())
	require.NotEqual(t, callB.String(), callC.String())
	legC1 := seedC2BLeg(t, store, callC, aLegID, "b-1", 1, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-1", "charge-a", "head-charge-a", 1, 30, true))
	correctedA := applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-1", "charge-a", "head-charge-a", 2, 25, true))
	require.Equal(t, int64(-5), correctedA.Delta.Nano)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-1", "charge-b", "head-charge-b", 1, 20, true))
	legC2 := seedC2BLeg(t, store, callC, aLegID, "b-2", 2, billing.LegOutcomeWinner)
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-2", "charge-z", "head-charge-z", 1, 10, true))
	applyC2BRevision(t, store, c2r2ChildRevisionInput(t, accountID, callC, aLegID, "b-2", "charge-z", "head-charge-z", 2, 10, false))
	claimC2BLegWork(t, store, accountID, callC, legC1)
	claimC2BLegWork(t, store, accountID, callC, legC2)
	down := phase10ProviderCost(3, 70, "USD")
	downSource, err := billing.CostPassThroughAdjustmentSourceKey(accountID, callB, down)
	require.NoError(t, err)
	applyALegPassThroughRevision(t, store, accountID, callB, down)

	// Old-token continuation: valid across appended evidence; originals
	// exactly once with no duplicate/omission; appended call at most once;
	// resumed pages carry live scope-wide totals with fresh AsOf.
	originals := map[string]bool{callA.String(): true, callB.String(): true}
	var resumeCalls, resumeLegs []string
	cursor := token
	resumedPages := 0
	var resumedFirst billing.ALegReport
	for {
		r := queryALegCustomerReport(t, store, accountID, aLegID, 1, cursor)
		if resumedPages == 0 {
			resumedFirst = r
		}
		resumedPages++
		require.Equal(t, 3, r.CallCount, "resumed page %d: CallCount is scope-wide live", resumedPages)
		require.Equal(t, int64(80), r.Retail.KnownSubtotal.Nano, "resumed page %d: totals are live", resumedPages)
		cycle3RequireAsOfObserved(t, page1, r)
		for _, row := range r.Calls {
			resumeCalls = append(resumeCalls, row.CallID.String())
		}
		for _, c := range r.Contributions {
			resumeLegs = append(resumeLegs, c.Call.CallID.String()+"\x00"+c.BLegID)
		}
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
		require.Less(t, resumedPages, 10, "old-token continuation must terminate")
	}
	require.False(t, resumedFirst.AsOf.Before(page1.AsOf))
	combinedCalls := append(append([]string(nil), page1Calls...), resumeCalls...)
	combinedLegs := append(append([]string(nil), page1Legs...), resumeLegs...)
	originalCount := 0
	for _, id := range combinedCalls {
		if originals[id] {
			originalCount++
		}
	}
	require.Equal(t, 2, originalCount, "old-token pages must yield each original call once, got %v", combinedCalls)
	cycle3RequireExactlyOnce(t, combinedCalls, "old-token call")
	cycle3RequireExactlyOnce(t, combinedLegs, "old-token contribution")
	newCallCount := 0
	for _, id := range combinedCalls {
		if id == callC.String() {
			newCallCount++
		}
	}
	require.LessOrEqual(t, newCallCount, 1, "old-token continuation must never duplicate the appended call")

	// Fresh later projection: all three calls/legs exactly once;
	// complete prior call DTOs byte-identical in business fields.
	freshCalls, freshLegs := cycle3UnionPages(t, store, accountID, aLegID, 1)
	require.Len(t, freshCalls, 3)
	cycle3RequireExactlyOnce(t, freshCalls, "fresh call")
	require.Len(t, freshLegs, 3, "b-a, b-1, b-2 (pass-through call B carries no B-legs)")
	cycle3RequireExactlyOnce(t, freshLegs, "fresh contribution")
	fresh := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, 3, fresh.CallCount)
	require.Equal(t, string(callADTOBefore), string(cycle3CallDTOBytes(t, fresh, callA)),
		"complete canonical call-A DTO must survive appended evidence byte-identical")
	summaryB := findALegCall(t, fresh, callB)
	require.Equal(t, billing.ALegCallKnown, summaryB.Status)
	require.Equal(t, int64(60), summaryB.CustomerCharge.Nano)
	require.Len(t, summaryB.Adjustments, 2)
	require.Equal(t, upSource, summaryB.Adjustments[0].TransactionID)
	require.Equal(t, int64(-20), summaryB.Adjustments[0].Amount.Nano)
	require.Equal(t, downSource, summaryB.Adjustments[1].TransactionID)
	require.Equal(t, int64(10), summaryB.Adjustments[1].Amount.Nano)
	resolvedC1 := findProviderLeg(t, fresh, callC, "b-1")
	require.Equal(t, billing.ALegProviderKnown, resolvedC1.ProviderStatus)
	require.Equal(t, int64(45), resolvedC1.ProviderCost.Nano)
	require.Len(t, resolvedC1.ProviderChildren, 2)
	require.Equal(t, "charge-a", resolvedC1.ProviderChildren[0].ChargeID)
	require.Equal(t, "charge-b", resolvedC1.ProviderChildren[1].ChargeID)
	require.Equal(t, billing.ALegProviderKnownZero, findProviderLeg(t, fresh, callC, "b-2").ProviderStatus)
	requireProviderLegTotals(t, fresh, 45, 1, 1, 1, 0)
	require.Equal(t, int64(80), fresh.Retail.KnownSubtotal.Nano)
	require.Equal(t, int64(45), fresh.Provider.KnownSubtotal.Nano)

	// Phase 3 (C): production customer reversal 20 -> 12 with replay,
	// stale, and loser fencing; usage execution untouched.
	sourceA, err := billing.CustomerSettlementSourceKey(accountID, callA)
	require.NoError(t, err)
	var usageCallsBefore int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM usage_call_records WHERE account_id = ?`, accountID).Scan(ctx, &usageCallsBefore))
	reversalInput := billing.JournalTransaction{
		ID: "tx-pg-c3-reversal", Book: billing.JournalBookFinancial, Currency: "USD", SourceKey: "src-tx-pg-c3-reversal",
		AccountID: accountID, TurnID: callA.String(), ALegID: aLegID, OperationKind: "customer_call_settlement",
		ReversalOf: sourceA,
		Entries: []billing.JournalEntry{
			{LedgerAccount: "usage_revenue", Side: billing.JournalDebit, Amount: billing.Money{Nano: 8, Currency: "USD"}},
			{LedgerAccount: "customer_financial_account", Side: billing.JournalCredit, Amount: billing.Money{Nano: 8, Currency: "USD"}},
		},
	}
	reversal, err := store.postJournalTransaction(ctx, reversalInput)
	require.NoError(t, err, "production writer must admit the linked customer reversal")
	require.Equal(t, sourceA, reversal.ReversalOf)
	require.Equal(t, sourceA, reversal.CorrectionGroupID, "writer derives the canonical correction group")
	corrected := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, int64(12), findALegCall(t, corrected, callA).CustomerCharge.Nano)
	require.Equal(t, int64(72), corrected.Retail.KnownSubtotal.Nano)
	require.Equal(t, []string{"b-a"}, findALegCall(t, corrected, callA).ExpectedBLegIDs)
	require.Equal(t, sourceA+":customer_call_settlement", findALegCall(t, corrected, callA).CustomerOperationKey)
	hashesAfterCorrection := cycle3PGSnapshotHashes(t, store)

	replayed, err := store.postJournalTransaction(ctx, reversalInput)
	require.NoError(t, err)
	require.Equal(t, reversal.ID, replayed.ID)
	require.Equal(t, reversal.AccountSequence, replayed.AccountSequence)
	require.Equal(t, hashesAfterCorrection, cycle3PGSnapshotHashes(t, store),
		"correction replay must not mutate durable contents")
	stale := reversalInput
	stale.ID = "tx-pg-c3-reversal-stale"
	stale.Entries = []billing.JournalEntry{
		{LedgerAccount: "usage_revenue", Side: billing.JournalDebit, Amount: billing.Money{Nano: 9, Currency: "USD"}},
		{LedgerAccount: "customer_financial_account", Side: billing.JournalCredit, Amount: billing.Money{Nano: 9, Currency: "USD"}},
	}
	_, err = store.postJournalTransaction(ctx, stale)
	require.ErrorIs(t, err, ErrIdentityConflict)
	require.Equal(t, hashesAfterCorrection, cycle3PGSnapshotHashes(t, store),
		"stale correction must not mutate durable contents")
	loser := reversalInput
	loser.ID = "tx-pg-c3-reversal-loser"
	loser.SourceKey = "src-tx-pg-c3-reversal-loser"
	_, err = store.postJournalTransaction(ctx, loser)
	require.ErrorIs(t, err, ErrCorrectionInvalid)
	require.Equal(t, hashesAfterCorrection, cycle3PGSnapshotHashes(t, store),
		"CAS-loser correction must not mutate durable contents")
	var usageCallsAfter int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM usage_call_records WHERE account_id = ?`, accountID).Scan(ctx, &usageCallsAfter))
	require.Equal(t, usageCallsBefore, usageCallsAfter, "late correction must not reopen call execution")
	steady := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
	require.Equal(t, string(cycle3BusinessJSON(t, corrected)), string(cycle3BusinessJSON(t, steady)))
	cycle3RequireAsOfObserved(t, corrected, steady)
	require.Equal(t, int64(20), findALegCall(t, preAppend, callA).CustomerCharge.Nano,
		"earlier copied DTO remains immutable")

	// Phase 4 (B): writer-silence idle/retirement with repeated reads:
	// identical business DTO, fresh non-decreasing AsOf, identical
	// full-content hashes over all 13 tables.
	previous := steady
	beforeBusiness := cycle3BusinessJSON(t, steady)
	for i := 0; i < 3; i++ {
		repeat := queryALegCustomerReport(t, store, accountID, aLegID, 0, "")
		require.Equal(t, string(beforeBusiness), string(cycle3BusinessJSON(t, repeat)),
			"idle repeat %d: business DTO must be identical", i)
		cycle3RequireAsOfObserved(t, previous, repeat)
		require.Equal(t, hashesAfterCorrection, cycle3PGSnapshotHashes(t, store),
			"idle repeat %d: durable contents must be identical", i)
		require.Equal(t, int64(72), repeat.Retail.KnownSubtotal.Nano)
		requireProviderLegTotals(t, repeat, 45, 1, 1, 1, 0)
		previous = repeat
	}
}
