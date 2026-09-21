package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 second-pass Finding 5 detail boundary contract: the durable
// economic-detail reader must retain the aggregate reconciliation plane so an
// operator can see the bounded classification outcome and the identity of the
// evidence that could not be reconciled, without the aggregate becoming a
// monetary amount.

func TestQueryEconomicDetailRetainsAggregatePlane(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-agg-detail", "USD")
	callID := edTestCallID(t)
	aLegID := "a-ed-agg-detail"
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-edAgg"))
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callID.String(), "b-edAgg")

	result := orTestMonetaryAggregateRetention(t, store.StoreID(), "rec-ed-agg-detail", 1, subject, time.Unix(1_700_400_000, 0).UTC())
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 1)
	entry := got.Reconciliations[0]
	require.Equal(t, metering.SubjectBLeg, entry.Subject.Kind)
	require.NotNil(t, entry.Monetary, "the monetary plane remains independent")
	require.NotNil(t, entry.Aggregate, "the retained aggregate plane must survive detail assembly")
	require.Len(t, entry.Aggregate.Rows, 1)
	require.Equal(t, []string{"end-to-end:USD"}, entry.Aggregate.Rows[0].MissingIDs)
	require.NotEmpty(t, entry.Aggregate.Rows[0].StatusCounts)
	require.Equal(t, "policy-agg-or", entry.Aggregate.Policy.ID)
	require.Equal(t, "v1", entry.Aggregate.Policy.Version)
}

func TestQueryEconomicDetailAggregateOnlyResultIsTruthful(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-agg-only", "USD")
	callID := edTestCallID(t)
	aLegID := "a-ed-agg-only"
	edSetupCall(t, store, account.ID, callID, aLegID, edTestLeg(t, "b-edAggOnly"))
	subject := edTestBLegSubject(store.StoreID(), "tenant-ed", account.ID, aLegID, callID.String(), "b-edAggOnly")

	result := orTestAggregateOnlyRetention(t, store.StoreID(), "rec-ed-agg-only", 1, subject, time.Unix(1_700_400_100, 0).UTC())
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), TenantID: "tenant-ed", AccountID: account.ID, BillingCallID: callID.String(), ALegID: aLegID,
	})
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 1)
	entry := got.Reconciliations[0]
	require.Nil(t, entry.Quantity, "an aggregate-only identity must not invent a quantity plane")
	require.Nil(t, entry.Monetary, "an aggregate-only identity must not invent a monetary plane")
	require.NotNil(t, entry.Aggregate)
	require.Equal(t, "policy-agg-or", entry.Aggregate.Policy.ID)
}
