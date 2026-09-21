package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Phase 16 second-pass Finding 1 public boundary proof: persistence is needed to
// show that otherwise complete retail and frozen selected COGS plus a pending
// allocation predecessor reach QueryEconomicDetail as a mutually consistent
// response. Before the fix the assembled margin claimed Complete=true while the
// same response reported an incomplete/non-payable allocation state.
func TestQueryEconomicDetailMarginIncompleteWithPendingAllocationPredecessor(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-margin-alloc", "USD")
	callID, _ := edCompleteCall(t, store, account.ID, "a-ed-margin-alloc", "edMA")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-margin-alloc", callID.String(), "b-edMA")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-ma",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	query := billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-margin-alloc",
	}
	before, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, before.Margin.Complete, "complete retail plus a frozen final COGS head is a complete margin")
	require.NotNil(t, before.Margin.Amount)

	// A replacement whose predecessor was never persisted makes the in-scope
	// allocation coverage pending and non-payable for the same call.
	amount := edTestDecimal(t, "10")
	pending := economics.AllocationRecord{
		ID: "alloc-ed-margin-v2", Version: 1,
		SourceSubject: edSupAllocationSource(store, account.ID, "shared-margin"),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationReplacement,
		Supersedes: []economics.AllocationRef{
			{StoreID: store.StoreID(), AllocationID: "alloc-ed-margin-v1", Version: 1, PayloadHash: strings.Repeat("f", 64)},
		},
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-ed-ma", Target: subject, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, pending))

	after, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.NotNil(t, after.Coverage.AllocationState, "pending ancestry must survive the public reader")
	require.Equal(t, economics.AllocationSupersessionPending, after.Coverage.AllocationState.Status)
	require.False(t, after.Coverage.AllocationState.Complete)
	require.False(t, after.Coverage.AllocationState.Payable)
	require.False(t, after.Margin.Complete, "pending allocation coverage must make the public detail margin incomplete")
	require.Nil(t, after.Margin.Amount, "an incomplete margin must not carry an amount")
	require.Equal(t, "allocation_incomplete", after.Margin.Reason)
}
