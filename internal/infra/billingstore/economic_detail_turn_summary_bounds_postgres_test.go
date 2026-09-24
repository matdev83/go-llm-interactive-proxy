//go:build integration

package billingstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestEconomicDetailSummaryHistoryBoundPostgresDirect proves the Finding 4
// bounded call-summary histories execute and fail closed on direct PostgreSQL
// with the same below-cap semantics as SQLite. It exercises both the journal
// transaction and the operation-snapshot bounded reads through the public
// reader.
func TestEconomicDetailSummaryHistoryBoundPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	account := edTestAccount(t, store, "ed-pg-summary-bounds", "USD")

	// Boundary: exactly the finite budget of each independent history is
	// accepted and the below-cap summary is unchanged.
	boundary := edTestCallID(t)
	edSetupCall(t, store, account.ID, boundary, "a-ed-pg-summary-boundary", edTestLeg(t, "b-ed-pg-summary-boundary"))
	edTestPlantJournalHistory(t, store, account.ID, boundary.String(), "a-ed-pg-summary-boundary", 0, economicDetailMaxJournalTransactions)
	edTestPlantSnapshotHistory(t, store, account.ID, boundary.String(), economicDetailMaxOperationSnapshots)

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: boundary.String(), ALegID: "a-ed-pg-summary-boundary",
	})
	require.NoError(t, err)
	require.NotNil(t, got.Summary.Turn)
	require.Equal(t, int64(economicDetailMaxJournalTransactions), got.Summary.Turn.CustomerCharge.Nano)
	require.True(t, got.Summary.Turn.Processed)

	// Overflow: one row over either history budget fails closed.
	overJournal := edTestCallID(t)
	edSetupCall(t, store, account.ID, overJournal, "a-ed-pg-summary-journal-over", edTestLeg(t, "b-ed-pg-summary-journal-over"))
	edTestPlantJournalHistory(t, store, account.ID, overJournal.String(), "a-ed-pg-summary-journal-over", 10000, economicDetailMaxJournalTransactions+1)
	_, err = store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: overJournal.String(), ALegID: "a-ed-pg-summary-journal-over",
	})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)

	overSnapshot := edTestCallID(t)
	edSetupCall(t, store, account.ID, overSnapshot, "a-ed-pg-summary-snapshot-over", edTestLeg(t, "b-ed-pg-summary-snapshot-over"))
	edTestPlantSnapshotHistory(t, store, account.ID, overSnapshot.String(), economicDetailMaxOperationSnapshots+1)
	_, err = store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: overSnapshot.String(), ALegID: "a-ed-pg-summary-snapshot-over",
	})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
}
