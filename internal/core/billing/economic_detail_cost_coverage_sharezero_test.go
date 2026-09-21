package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Phase 16 sixth-pass Finding 2 core contract: allocation coverage classifies
// the exact attributable contribution of one allocation *target*, never its
// shared source aggregate. A conserved, legal zero-weight target of a nonzero
// shared source incurs exactly zero attributable cost and is a canonical
// known-zero exemption. A target with a nonzero exact share stays
// inclusion-required even when its integer ledger projection floors to zero,
// and a declared residual attributed to a zero-weight target is real money that
// can never be exempted from its raw weight.

// finding2ShareLine builds one monetary allocation line for the canonical scope
// carrying an explicit exact source amount, exact share and canonical rounded
// projection, so the classification can distinguish an exact zero from a
// rounding-to-zero of a nonzero contribution.
func finding2ShareLine(t *testing.T, storeID, accountID, aLegID, billingCallID, bLegID, sourceAmount string, share economics.AllocationFraction, rounded *economics.AllocationRoundedAmount) AllocatedCostLine {
	t.Helper()
	line := detailTestAllocationLine(storeID, accountID, aLegID, billingCallID, bLegID)
	line.AllocationID = "alloc-f2-share"
	line.AllocationVersion = 1
	line.PayloadHash = strings.Repeat("a", 64)
	line.TargetID = "t-f2-" + bLegID
	if sourceAmount != "" {
		value := detailTestDecimal(t, sourceAmount)
		line.SourceAmount = &value
	}
	line.Weight = share
	line.Share = share
	line.RoundedAmount = rounded
	return line
}

func finding2Rounded(nano int64) *economics.AllocationRoundedAmount {
	return &economics.AllocationRoundedAmount{NanoUnits: nano, Currency: "USD", Present: true, Policy: economics.RoundingHalfEven}
}

func finding2CoverageByTarget(t *testing.T, got EconomicDetail) map[string]EconomicDetailAllocationCoverage {
	t.Helper()
	require.NotNil(t, got.Coverage.CostCoverage)
	out := make(map[string]EconomicDetailAllocationCoverage, len(got.Coverage.CostCoverage.AllocationCoverage))
	for _, entry := range got.Coverage.CostCoverage.AllocationCoverage {
		out[entry.TargetID] = entry
	}
	return out
}

// TestAssembleEconomicDetailFinding2ZeroShareTargetFromNonzeroSourceKeepsMarginComplete
// is the exact sixth-pass reproduction: a conserved USD 10 source allocates
// 0/1 to one target and 1/1 to a sibling target outside this query scope. The
// in-scope target incurs exactly zero attributable cost, so it is a canonical
// known-zero exemption rather than an unresolved allocation.
func TestAssembleEconomicDetailFinding2ZeroShareTargetFromNonzeroSourceKeepsMarginComplete(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	in.Allocations = []AllocatedCostLine{
		finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "10",
			economics.AllocationFraction{Numerator: "0", Denominator: "1"}, finding2Rounded(0)),
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.True(t, got.Coverage.CostCoverage.Complete, "an exact zero attributable share cannot block cost coverage")
	require.Equal(t, 0, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, EconomicDetailCostCoverageKnownZero, entry.State)
	require.Equal(t, EconomicDetailAllocationCoverageKnownZero, entry.Reason)
	require.True(t, got.Margin.Complete)
	require.Equal(t, "complete", got.Margin.Reason)
}

// TestAssembleEconomicDetailFinding2NonzeroShareSiblingStaysInclusionRequired
// proves the fix is per target: a nonzero-share sibling of the same conserved
// source remains a live attributable cost and must be proven included.
func TestAssembleEconomicDetailFinding2NonzeroShareSiblingStaysInclusionRequired(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	zero := finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "10",
		economics.AllocationFraction{Numerator: "0", Denominator: "1"}, finding2Rounded(0))
	sibling := finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-2", "10",
		economics.AllocationFraction{Numerator: "1", Denominator: "1"}, finding2Rounded(10_000_000_000))
	sibling.AllocationID = zero.AllocationID
	sibling.AllocationVersion = zero.AllocationVersion
	sibling.PayloadHash = zero.PayloadHash

	in := detailTestCompleteInput(t)
	in.Allocations = []AllocatedCostLine{zero, sibling}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount, "only the nonzero-share target is inclusion-required")
	byTarget := finding2CoverageByTarget(t, got)
	require.Equal(t, EconomicDetailCostCoverageKnownZero, byTarget[zero.TargetID].State)
	require.Equal(t, EconomicDetailCostCoverageUnresolved, byTarget[sibling.TargetID].State)
	require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, byTarget[sibling.TargetID].Reason)
	require.False(t, got.Margin.Complete)
	require.Equal(t, "allocation_coverage_unresolved", got.Margin.Reason)
}

// TestAssembleEconomicDetailFinding2RoundedZeroNonzeroShareStaysUnresolved
// proves the approved exact allocation semantics: an exact nonzero contribution
// that merely floors to a zero integer projection is still real cost and must
// not be exempted as known zero.
func TestAssembleEconomicDetailFinding2RoundedZeroNonzeroShareStaysUnresolved(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	in.Allocations = []AllocatedCostLine{
		finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "0.000000001",
			economics.AllocationFraction{Numerator: "1", Denominator: "3"}, finding2Rounded(0)),
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete, "a rounded zero hiding a nonzero exact share must stay unresolved")
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, entry.Reason)
}

// TestAssembleEconomicDetailFinding2ZeroShareResidualStaysUnresolved proves the
// canonical rounded projection is authoritative when it disagrees with the raw
// weight: a residual actually attributed to a zero-weight target is live money
// and must stay inclusion-required instead of being exempted by weight alone.
func TestAssembleEconomicDetailFinding2ZeroShareResidualStaysUnresolved(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	in.Allocations = []AllocatedCostLine{
		finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "10",
			economics.AllocationFraction{Numerator: "0", Denominator: "1"}, finding2Rounded(1)),
	}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, EconomicDetailAllocationCoverageNotIncluded, entry.Reason)
}

// TestAssembleEconomicDetailFinding2ZeroSourceOrZeroShareKnownZero proves both
// exact-zero factor semantics: an exact zero source and an exact zero share are
// equally canonical known-zero exemptions.
func TestAssembleEconomicDetailFinding2ZeroSourceOrZeroShareKnownZero(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	for _, tc := range []struct {
		name   string
		amount string
		share  economics.AllocationFraction
		adjust func(*AllocatedCostLine)
	}{
		{name: "zero source full share", amount: "0", share: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		{name: "nonzero source zero share without rounded projection", amount: "10", share: economics.AllocationFraction{Numerator: "0", Denominator: "1"},
			adjust: func(line *AllocatedCostLine) { line.RoundedAmount = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := detailTestCompleteInput(t)
			line := finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", tc.amount, tc.share, finding2Rounded(0))
			if tc.adjust != nil {
				tc.adjust(&line)
			}
			in.Allocations = []AllocatedCostLine{line}

			got, err := AssembleEconomicDetail(in)
			require.NoError(t, err)
			require.True(t, got.Coverage.CostCoverage.Complete)
			require.Equal(t, 0, got.Coverage.CostCoverage.AllocationUnresolvedCount)
			require.Equal(t, EconomicDetailCostCoverageKnownZero, got.Coverage.CostCoverage.AllocationCoverage[0].State)
			require.True(t, got.Margin.Complete)
		})
	}
}

// TestAssembleEconomicDetailFinding2RedactedZeroShareDoesNotLeakOrExempt proves
// a redacted source can never gain a zero exemption from its weight and never
// leaks an amount or currency through coverage.
func TestAssembleEconomicDetailFinding2RedactedZeroShareDoesNotLeakOrExempt(t *testing.T) {
	t.Parallel()
	storeID, accountID, aLegID, billingCallID := detailTestScope()

	in := detailTestCompleteInput(t)
	line := finding2ShareLine(t, storeID, accountID, aLegID, billingCallID, "b-detail-1", "",
		economics.AllocationFraction{Numerator: "0", Denominator: "1"}, nil)
	line.SourceAmount = nil
	line.RoundedAmount = nil
	line.Redacted = true
	in.Allocations = []AllocatedCostLine{line}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, EconomicDetailAllocationCoverageRedactedSource, entry.Reason)
	require.True(t, entry.Redacted)
	require.Empty(t, entry.Currency)
	require.False(t, got.Margin.Complete)
}
