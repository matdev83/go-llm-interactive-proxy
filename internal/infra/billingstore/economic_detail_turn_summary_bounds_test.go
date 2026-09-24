package billingstore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// Finding 4 RED contract: the economic-detail call settlement summary must not
// materialize an unbounded call journal or operation-snapshot correction
// history. Both independent histories must carry a deterministic database-side
// ORDER BY plus LIMIT budget+1 and fail closed with
// billing.ErrEconomicDetailBoundExceeded before any row decode or secondary
// journal-entry load, instead of growing slices/maps with an arbitrarily long
// correction history while every other detail cap stays under limit.

func edTestPlantJournalHistory(t *testing.T, store *DurableStore, accountID, callID, aLegID string, seqBase uint64, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		plantALegSettlementJournal(t, store, fmt.Sprintf("jrn-%s-%05d", callID, i), accountID,
			callID, aLegID, "customer_call_settlement",
			"customer_financial_account", "usage_revenue", 1, seqBase+uint64(i+1), "", "", "")
	}
}

func edTestPlantSnapshotHistory(t *testing.T, store *DurableStore, accountID, callID string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		// The durable writer enforces UNIQUE(account_id, operation_kind,
		// source_key), so a long operation history varies the kind. The first
		// row stays a mapped settlement kind so a below-cap summary is
		// meaningful; the rest still count toward the same finite row budget.
		// Keys are call-scoped so multiple calls in one store never collide on
		// the primary key.
		kind := "customer_call_settlement"
		if i > 0 {
			kind = fmt.Sprintf("ed_correction_kind_%05d", i)
		}
		plantALegSettlementMarker(t, store, fmt.Sprintf("op-%s-%05d", callID, i), accountID,
			kind, callID, "fp-ed-hist", "integrity-ed-hist")
	}
}

func edTestQueryCallSummary(t *testing.T, store *DurableStore, accountID string, callID billing.BillingCallID, aLegID string) (billing.EconomicDetail, error) {
	t.Helper()
	return store.QueryEconomicDetail(context.Background(), billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: accountID, BillingCallID: callID.String(), ALegID: aLegID,
	})
}

// TestEconomicDetailCallJournalHistoryBoundFailsClosed proves a single call
// with more financial journal rows than the finite detail budget fails closed
// through the public reader rather than materializing the whole history.
func TestEconomicDetailCallJournalHistoryBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-summary-journal-over", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-summary-journal-over", edTestLeg(t, "b-ed-summary-journal-over"))
	edTestPlantJournalHistory(t, store, account.ID, callID.String(), "a-ed-summary-journal-over", 0, economicDetailMaxJournalTransactions+1)

	_, err := edTestQueryCallSummary(t, store, account.ID, callID, "a-ed-summary-journal-over")
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}

// TestEconomicDetailCallOperationSnapshotHistoryBoundFailsClosed proves a
// single call with more settlement operation snapshots than the finite detail
// budget fails closed through the public reader.
func TestEconomicDetailCallOperationSnapshotHistoryBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-summary-snapshot-over", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-summary-snapshot-over", edTestLeg(t, "b-ed-summary-snapshot-over"))
	edTestPlantSnapshotHistory(t, store, account.ID, callID.String(), economicDetailMaxOperationSnapshots+1)

	_, err := edTestQueryCallSummary(t, store, account.ID, callID, "a-ed-summary-snapshot-over")
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}

// TestEconomicDetailCallJournalHistoryBoundaryPasses proves exactly the finite
// journal budget is still accepted and the below-cap summary is unchanged: one
// usage_revenue credit unit per row nets a revenue of exactly the budget.
func TestEconomicDetailCallJournalHistoryBoundaryPasses(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-summary-journal-max", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-summary-journal-max", edTestLeg(t, "b-ed-summary-journal-max"))
	edTestPlantJournalHistory(t, store, account.ID, callID.String(), "a-ed-summary-journal-max", 0, economicDetailMaxJournalTransactions)

	got, err := edTestQueryCallSummary(t, store, account.ID, callID, "a-ed-summary-journal-max")
	require.NoError(t, err)
	require.NotNil(t, got.Summary.Turn)
	require.Equal(t, int64(economicDetailMaxJournalTransactions), got.Summary.Turn.CustomerCharge.Nano)
	require.Zero(t, got.Summary.Turn.ProviderCost.Nano)
}

// TestEconomicDetailCallOperationSnapshotHistoryBoundaryPasses proves exactly
// the finite snapshot budget is still accepted and still marks the call
// processed below cap.
func TestEconomicDetailCallOperationSnapshotHistoryBoundaryPasses(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-summary-snapshot-max", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-summary-snapshot-max", edTestLeg(t, "b-ed-summary-snapshot-max"))
	edTestPlantSnapshotHistory(t, store, account.ID, callID.String(), economicDetailMaxOperationSnapshots)

	got, err := edTestQueryCallSummary(t, store, account.ID, callID, "a-ed-summary-snapshot-max")
	require.NoError(t, err)
	require.NotNil(t, got.Summary.Turn)
	require.True(t, got.Summary.Turn.Processed)
}

// TestEconomicDetailSummaryHistoryQueriesCarryDatabaseSideLimit proves both
// independent histories are bounded in SQL itself (deterministic ORDER BY plus
// a database-side LIMIT), so the bound cannot be enforced only after Go growth.
func TestEconomicDetailSummaryHistoryQueriesCarryDatabaseSideLimit(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	account := edTestAccount(t, store, "ed-summary-limit", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-summary-limit", edTestLeg(t, "b-ed-summary-limit"))
	edTestPlantJournalHistory(t, store, account.ID, callID.String(), "a-ed-summary-limit", 0, 1)
	edTestPlantSnapshotHistory(t, store, account.ID, callID.String(), 1)

	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	_, err := edTestQueryCallSummary(t, store, account.ID, callID, "a-ed-summary-limit")
	require.NoError(t, err)

	journalBounded, snapshotBounded := false, false
	for _, query := range recorder.queries {
		if strings.Contains(query, "journal_transactions") && strings.Contains(query, "ORDER BY") && strings.Contains(query, "LIMIT") {
			journalBounded = true
		}
		if strings.Contains(query, "billing_operation_snapshots") && strings.Contains(query, "ORDER BY") && strings.Contains(query, "LIMIT") {
			snapshotBounded = true
		}
	}
	require.True(t, journalBounded, "journal SQL must carry ORDER BY + LIMIT; recorded: %v", recorder.queries)
	require.True(t, snapshotBounded, "operation-snapshot SQL must carry ORDER BY + LIMIT; recorded: %v", recorder.queries)
}
