package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 fifth-pass Finding 3 core contract: an active, complete/payable,
// positive attributable monetary allocation must participate in selected-cost
// inclusion proof. It is never inferred included from its target B-leg, its
// complete/payable lineage state or a matching currency, and its amount is never
// summed into the selected total or margin. It is proven included only when the
// exact frozen selected valuation names its immutable allocation revision and
// payload hash; an explicit exact zero is the only monetary exemption.

// finding3AllocationLine builds one positive (or explicit-zero) monetary
// allocation contribution for the canonical detail scope, carrying an exact
// source amount and immutable payload hash so a selected-coverage reference can
// be verified against it.
func finding3AllocationLine(t *testing.T, storeID, accountID, aLegID, billingCallID, bLegID, currency, amount string) AllocatedCostLine {
	t.Helper()
	line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, bLegID)
	line.AllocationID = "alloc-finding3"
	line.AllocationVersion = 1
	line.PayloadHash = strings.Repeat("a", 64)
	line.Policy = economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)}
	if amount != "" {
		value := detailTestDecimal(t, amount)
		line.SourceAmount = &value
	}
	line.Currency = currency
	return line
}

// finding3SelectedAllocationRef binds the exact immutable allocation identity
// the selected valuation must name to prove inclusion.
func finding3SelectedAllocationRef(storeID string, line AllocatedCostLine) economics.AllocationRef {
	return economics.AllocationRef{
		StoreID: storeID, AllocationID: line.AllocationID,
		Version: line.AllocationVersion, PayloadHash: line.PayloadHash,
	}
}

// finding3BindAllocationCoverage attaches the explicit inclusion reference to
// the winning frozen selected valuation (valuation-p) without disturbing any
// other plane.
func finding3BindAllocationCoverage(t *testing.T, in *EconomicDetailInput, ref economics.AllocationRef) {
	t.Helper()
	bound := false
	for i := range in.Valuations {
		if in.Valuations[i].ID != "valuation-p" {
			continue
		}
		in.Valuations[i].AllocationCoverageRefs = []economics.AllocationRef{ref}
		require.NoError(t, in.Valuations[i].Validate())
		bound = true
	}
	require.True(t, bound, "the winning selected valuation must be present")
}

// TestAssembleEconomicDetailFinding3ActiveAllocationAbsentIncomplete is the
// exact fifth-pass reproduction: a normal active positive USD 0.25 allocation
// targeting the in-scope B-leg is not included by the explicit USD 1.32 selected
// amount, so the margin must stay incomplete even though the allocation lineage
// state is complete/payable.
func TestAssembleEconomicDetailFinding3ActiveAllocationAbsentIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	control := detailTestCompleteInput(t)
	complete, err := AssembleEconomicDetail(control)
	require.NoError(t, err)
	require.True(t, complete.Margin.Complete)

	in := detailTestCompleteInput(t)
	in.Allocations = []AllocatedCostLine{
		finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25"),
	}
	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.False(t, got.Coverage.CostCoverage.Complete, "an unproven positive allocation cannot support complete cost coverage")
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, "alloc-finding3", entry.AllocationID)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, entry.Reason)

	require.False(t, got.Margin.Complete, "a cost omitted from the selected amount cannot produce a complete margin")
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "allocation_coverage_unresolved", got.Margin.Reason)
	// The allocation amount is never summed into the selected total.
	require.Equal(t, complete.Totals.SelectedAmount.Decimal.CanonicalString(), got.Totals.SelectedAmount.Decimal.CanonicalString())
}

// TestAssembleEconomicDetailFinding3CurrencyIncompatibleAllocationIncomplete
// proves a currency-incompatible active allocation cannot be treated as
// included merely because a reference exists, and reports a truthful reason.
func TestAssembleEconomicDetailFinding3CurrencyIncompatibleAllocationIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "EUR", "0.25")
	in.Allocations = []AllocatedCostLine{line}
	// Even an exact inclusion reference cannot cross currencies without a
	// frozen conversion basis.
	finding3BindAllocationCoverage(t, &in, finding3SelectedAllocationRef(storeID, line))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, got.Coverage.CostCoverage.AllocationCoverage[0].State)
	require.Equal(t, EconomicDetailAllocationCoverageCurrencyMismatch, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
	require.False(t, got.Margin.Complete)
	require.Nil(t, got.Margin.Amount)
	require.Equal(t, "allocation_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailFinding3RedactedAllocationFailsClosed proves a
// redacted account-less source cannot be silently treated as included or as
// zero, fails closed, and never leaks an amount.
func TestAssembleEconomicDetailFinding3RedactedAllocationFailsClosed(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, "b-detail-1")
	line.AllocationID = "alloc-finding3-redacted"
	line.AllocationVersion = 1
	line.Redacted = true
	in.Allocations = []AllocatedCostLine{line}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, EconomicDetailAllocationCoverageRedactedSource, entry.Reason)
	require.True(t, entry.Redacted)
	require.Empty(t, entry.Currency, "a redacted source must not leak even its currency through coverage")
	require.Nil(t, got.Coverage.Allocations[0].SourceAmount)
	require.False(t, got.Margin.Complete)
	require.Equal(t, "allocation_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailFinding3NonLiveAllocationSemanticsStayTruthful
// proves informational linkages and unallocated remainders are not live
// attributed costs and never block a complete margin.
func TestAssembleEconomicDetailFinding3NonLiveAllocationSemanticsStayTruthful(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	t.Run("informational linkage does not block", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
		line.Informational = true
		in.Allocations = []AllocatedCostLine{line}

		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		require.Empty(t, got.Coverage.CostCoverage.AllocationCoverage)
	})

	t.Run("unallocated remainder does not block", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
		line.Unallocated = true
		line.Target = metering.SubjectRef{}
		line.TargetID = "t-unallocated"
		in.Allocations = []AllocatedCostLine{line}

		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		require.Empty(t, got.Coverage.CostCoverage.AllocationCoverage)
	})

	t.Run("quantity-only allocation does not move a monetary margin", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "", "")
		quantity := detailTestDecimal(t, "5")
		line.SourceAmount = nil
		line.SourceQuantity = &quantity
		line.Unit = metering.UnitCount
		in.Allocations = []AllocatedCostLine{line}

		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.True(t, got.Margin.Complete)
		require.Empty(t, got.Coverage.CostCoverage.AllocationCoverage)
	})
}

// TestAssembleEconomicDetailFinding3ExplicitZeroAllocationDoesNotBlock proves an
// exact canonical zero allocation is a truthful exemption, not an unresolved
// cost.
func TestAssembleEconomicDetailFinding3ExplicitZeroAllocationDoesNotBlock(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	in.Allocations = []AllocatedCostLine{
		finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0"),
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Margin.Complete)
	require.Equal(t, 0, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	require.Equal(t, EconomicDetailCostCoverageKnownZero, got.Coverage.CostCoverage.AllocationCoverage[0].State)
	require.Equal(t, EconomicDetailAllocationCoverageKnownZero, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
}

// TestAssembleEconomicDetailFinding3ProvenIncludedAllocationCompletesWithoutDoubleCounting
// is the positive control: an exact selected allocation-coverage reference with
// a matching immutable payload hash and currency proves inclusion without adding
// the allocation amount to the selected subtotal or margin.
func TestAssembleEconomicDetailFinding3ProvenIncludedAllocationCompletesWithoutDoubleCounting(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	control, err := AssembleEconomicDetail(detailTestCompleteInput(t))
	require.NoError(t, err)
	require.True(t, control.Margin.Complete)

	in := detailTestCompleteInput(t)
	line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
	in.Allocations = []AllocatedCostLine{line}
	finding3BindAllocationCoverage(t, &in, finding3SelectedAllocationRef(storeID, line))

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Margin.Complete, "an exactly proven allocation must not block completeness")
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, "complete", got.Margin.Reason)
	require.Equal(t, 0, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Equal(t, EconomicDetailAllocationCoverageIncluded, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
	require.Equal(t, EconomicDetailCostCoverageSelected, got.Coverage.CostCoverage.AllocationCoverage[0].State)
	// No double counting: selected subtotal and margin are byte-identical to the
	// control that never saw the allocation.
	require.Equal(t, control.Margin.Amount.Decimal.CanonicalString(), got.Margin.Amount.Decimal.CanonicalString())
	require.Equal(t, control.Totals.SelectedAmount.Decimal.CanonicalString(), got.Totals.SelectedAmount.Decimal.CanonicalString())
}

// TestAssembleEconomicDetailFinding3StaleOrWrongTargetAllocationIncomplete
// proves a reference only proves the exact revision and the exact B-leg
// ownership: a wrong payload hash, a wrong target and a missing allocation all
// fail closed.
func TestAssembleEconomicDetailFinding3StaleOrWrongTargetAllocationIncomplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	t.Run("wrong payload hash fails closed", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
		in.Allocations = []AllocatedCostLine{line}
		ref := finding3SelectedAllocationRef(storeID, line)
		ref.PayloadHash = strings.Repeat("b", 64)
		finding3BindAllocationCoverage(t, &in, ref)

		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.False(t, got.Margin.Complete)
		require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
	})

	t.Run("wrong target leg fails closed", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
		line.Target = detailTestSubject(storeID, aLegID, billingCallID, "b-finding3-other")
		in.Allocations = []AllocatedCostLine{line}
		finding3BindAllocationCoverage(t, &in, finding3SelectedAllocationRef(storeID, line))

		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.False(t, got.Margin.Complete)
		require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
	})

	t.Run("reference to an absent allocation proves nothing", func(t *testing.T) {
		t.Parallel()
		in := detailTestCompleteInput(t)
		line := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
		line.AllocationID = "alloc-finding3-present"
		in.Allocations = []AllocatedCostLine{line}
		ref := finding3SelectedAllocationRef(storeID, line)
		ref.AllocationID = "alloc-finding3-absent"
		finding3BindAllocationCoverage(t, &in, ref)

		got, err := AssembleEconomicDetail(in)
		require.NoError(t, err)
		require.False(t, got.Margin.Complete)
		require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
	})
}

// TestAssembleEconomicDetailFinding3SnapshotInvalidatesAllocationChange proves
// allocation coverage is part of the repeated full-scope snapshot, so adding an
// allocation changes the fingerprint even when every other fact is equal.
func TestAssembleEconomicDetailFinding3SnapshotInvalidatesAllocationChange(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	base, err := AssembleEconomicDetail(detailTestCompleteInput(t))
	require.NoError(t, err)

	withAllocation := detailTestCompleteInput(t)
	withAllocation.Allocations = []AllocatedCostLine{
		finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25"),
	}
	changed, err := AssembleEconomicDetail(withAllocation)
	require.NoError(t, err)
	require.NotEqual(t, base.SnapshotFingerprint, changed.SnapshotFingerprint)
}

// TestAssembleEconomicDetailFinding3DeterministicOrdering proves allocation
// coverage is deterministically ordered regardless of input order.
func TestAssembleEconomicDetailFinding3DeterministicOrdering(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	first := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.25")
	second := finding3AllocationLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "USD", "0.50")
	second.AllocationID = "alloc-finding3-b"

	forward := detailTestCompleteInput(t)
	forward.Allocations = []AllocatedCostLine{first, second}
	reverse := detailTestCompleteInput(t)
	reverse.Allocations = []AllocatedCostLine{second, first}

	gotForward, err := AssembleEconomicDetail(forward)
	require.NoError(t, err)
	gotReverse, err := AssembleEconomicDetail(reverse)
	require.NoError(t, err)
	require.Equal(t, gotForward.SnapshotFingerprint, gotReverse.SnapshotFingerprint)
	require.Len(t, gotForward.Coverage.CostCoverage.AllocationCoverage, 2)
	require.Equal(t, "alloc-finding3", gotForward.Coverage.CostCoverage.AllocationCoverage[0].AllocationID)
	require.Equal(t, "alloc-finding3-b", gotForward.Coverage.CostCoverage.AllocationCoverage[1].AllocationID)
}
