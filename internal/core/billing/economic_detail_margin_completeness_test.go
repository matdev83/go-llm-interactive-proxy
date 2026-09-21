package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Phase 16 second-pass Finding 1: margin completeness must be derived from the
// full assembled scope. A frozen final selected COGS head plus a complete retail
// valuation must not assert Margin.Complete when a same-scope allocation
// lineage is pending/non-payable, when any independent reconciliation identity
// is incomplete, or when several independent retail identities exist. The
// amount stays anchored to one unambiguous selected head and the approved retail
// projection; overlapping independent heads are never summed.

func marginTestAllocationRef(storeID string) economics.AllocationRef {
	return economics.AllocationRef{
		StoreID: storeID, AllocationID: "alloc-margin-missing-v1",
		Version: 1, PayloadHash: strings.Repeat("f", 64),
	}
}

// marginTestPendingAllocationState builds the truthful correction state a
// missing supersession predecessor produces: pending, incomplete, non-payable.
func marginTestPendingAllocationState(storeID string) *EconomicDetailAllocationState {
	ref := marginTestAllocationRef(storeID)
	return &EconomicDetailAllocationState{
		Status: economics.AllocationSupersessionPending, Complete: false, Payable: false,
		Pending: []economics.AllocationRef{ref},
	}
}

// TestAssembleEconomicDetailMarginIncompleteWithPendingAllocation proves the
// concrete Finding 1 contradiction cannot occur: otherwise complete retail and
// selected COGS plus a pending/missing allocation predecessor must report an
// incomplete margin with a truthful reason, while a resolved payable allocation
// keeps the same margin complete.
func TestAssembleEconomicDetailMarginIncompleteWithPendingAllocation(t *testing.T) {
	t.Parallel()
	storeID, _, _, _ := detailTestScope()

	control := detailTestCompleteInput(t)
	complete, err := AssembleEconomicDetail(control)
	require.NoError(t, err)
	require.True(t, complete.Margin.Complete, "frozen final COGS plus complete retail is a complete margin")
	require.NotNil(t, complete.Margin.Amount)

	pending := detailTestCompleteInput(t)
	pending.AllocationState = marginTestPendingAllocationState(storeID)
	got, err := AssembleEconomicDetail(pending)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.AllocationState, "pending ancestry must survive assembly")
	require.False(t, got.Coverage.AllocationState.Complete)
	require.False(t, got.Coverage.AllocationState.Payable)
	require.False(t, got.Margin.Complete, "incomplete COGS allocation coverage cannot support a complete margin")
	require.Nil(t, got.Margin.Amount, "an incomplete margin must not carry an amount")
	require.Equal(t, "allocation_incomplete", got.Margin.Reason)

	resolved := detailTestCompleteInput(t)
	resolved.AllocationState = &EconomicDetailAllocationState{
		Status: economics.AllocationSupersessionResolved, Complete: true, Payable: true,
	}
	ok, err := AssembleEconomicDetail(resolved)
	require.NoError(t, err)
	require.True(t, ok.Margin.Complete, "a resolved payable allocation keeps the margin complete")
	require.NotNil(t, ok.Margin.Amount)
	require.Equal(t, "complete", ok.Margin.Reason)
}

// TestAssembleEconomicDetailMarginRequiresCompleteReconciliationSet proves the
// full authoritative reconciliation set, not only the singular-first projection,
// gates margin completeness. Any independent later identity with an incomplete
// or absent comparison plane makes the margin explicitly incomplete.
func TestAssembleEconomicDetailMarginRequiresCompleteReconciliationSet(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subjectOne := detailTestSubject(storeID, aLegID, billingCallID, "b-margin-rec-1")
	subjectTwo := detailTestSubject(storeID, aLegID, billingCallID, "b-margin-rec-2")
	completeEntry := detailTestReconciliation(t, storeID, subjectOne, ReconciliationStatusDiscrepant)
	require.NotNil(t, completeEntry.Monetary)
	require.Equal(t, MonetaryDiscrepancyComplete, completeEntry.Monetary.Status)

	t.Run("single complete reconciliation keeps margin complete", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.Quantity, in.Monetary = nil, nil
		in.Reconciliations = []EconomicDetailReconciliation{completeEntry}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		require.NotNil(t, got.Margin.Amount)
	})

	t.Run("later incomplete reconciliation makes margin incomplete", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.Quantity, in.Monetary = nil, nil
		incomplete := detailTestReconciliation(t, storeID, subjectTwo, ReconciliationStatusIncomparable)
		in.Reconciliations = []EconomicDetailReconciliation{completeEntry, incomplete}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.Len(t, got.Reconciliations, 2)
		require.False(t, got.Margin.Complete, "a later incomplete reconciliation identity cannot be ignored")
		require.Nil(t, got.Margin.Amount)
		require.Equal(t, "comparison_incomparable", got.Margin.Reason)
	})

	t.Run("plane-less reconciliation identity is unproven", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		in.Quantity, in.Monetary = nil, nil
		in.Reconciliations = []EconomicDetailReconciliation{{Subject: subjectTwo}}
		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.False(t, got.Margin.Complete, "an unrepresented reconciliation identity cannot be treated as complete")
		require.Nil(t, got.Margin.Amount)
		require.Equal(t, "comparison_incomparable", got.Margin.Reason)
	})
}

// TestAssembleEconomicDetailMarginIncompleteWithMultipleRetailIdentities proves
// independent retail (customer-policy) valuation identities are never collapsed
// into one complete margin. No single truthful margin exists for the scope, so
// no amount is projected.
func TestAssembleEconomicDetailMarginIncompleteWithMultipleRetailIdentities(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	in := detailTestCompleteInput(t)
	otherSubject := detailTestSubject(storeID, aLegID, billingCallID, "b-margin-retail-2")
	in.Valuations = append(in.Valuations, detailTestValuation(
		t, "valuation-r-margin-2", economics.BasisCustomerPolicy, otherSubject, storeID,
		detailTestCurrencyTotal(t, "USD", "3.00"),
	))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Margin.Complete, "independent retail identities cannot be collapsed into one margin")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "retail_ambiguous", got.Margin.Reason)
}

// TestAssembleEconomicDetailMarginOverlappingHeadsNeverSummed proves the
// full-scope completeness derivation never introduces a second monetary
// authority: overlapping independent selected heads remain an explicit
// ambiguous scope even when allocation coverage is complete, and their amounts
// are never summed.
func TestAssembleEconomicDetailMarginOverlappingHeadsNeverSummed(t *testing.T) {
	t.Parallel()
	in := detailTestCompleteInput(t)
	in.Selection = nil
	second := in.Heads[0].Clone()
	second.HeadKey = "head-p-overlap"
	in.Heads = []SelectedCostHead{in.Heads[0], second}
	in.AllocationState = &EconomicDetailAllocationState{
		Status: economics.AllocationSupersessionResolved, Complete: true, Payable: true,
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.Heads, 2)
	require.True(t, got.Totals.SelectedAmbiguous)
	require.Nil(t, got.Totals.SelectedAmount, "overlapping heads are never summed")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "ambiguous_selected", got.Margin.Reason)
	require.Nil(t, got.Margin.Amount)
}
