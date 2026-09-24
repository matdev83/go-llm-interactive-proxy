package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 sixth-pass Finding 2 durable boundary proof: QueryEconomicDetail
// classifies each active allocation line by the exact attributable contribution
// of its own target, never by the shared source aggregate. A conserved legal
// zero-weight target of a nonzero source is a canonical known-zero exemption;
// a nonzero-share sibling stays inclusion-required; a rounded zero of a nonzero
// exact share stays unresolved; a redacted source never gains a zero exemption
// or leaks economics. No allocation amount is summed into the selected amount.

func edFinding2ShareAllocation(t *testing.T, store *DurableStore, accountID, resourceID, currency, amount string, targets ...economics.AllocationTarget) economics.AllocationRecord {
	t.Helper()
	value := edTestDecimal(t, amount)
	return economics.AllocationRecord{
		ID: "alloc-f2-" + resourceID, Version: 1,
		SourceSubject: edSupAllocationSource(store, accountID, resourceID),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &value, Currency: currency,
		Policy:        edSupAllocationPolicy(),
		Operation:     economics.AllocationOperationAllocate,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets:                targets,
		CreatedAt:              time.Unix(300, 0).UTC(),
	}
}

func edFinding2Weight(numerator, denominator string) economics.AllocationFraction {
	return economics.AllocationFraction{Numerator: numerator, Denominator: denominator}
}

func edFinding2CallTarget(store *DurableStore, accountID, aLegID, callID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectBillingCall, StoreID: store.StoreID(), TenantID: edSupTenant, AccountID: accountID,
		ALegID: aLegID, BillingCallID: callID,
	}
}

func edFinding2CoverageByTarget(t *testing.T, got billing.EconomicDetail) map[string]billing.EconomicDetailAllocationCoverage {
	t.Helper()
	require.NotNil(t, got.Coverage.CostCoverage)
	out := make(map[string]billing.EconomicDetailAllocationCoverage, len(got.Coverage.CostCoverage.AllocationCoverage))
	for _, entry := range got.Coverage.CostCoverage.AllocationCoverage {
		out[entry.TargetID] = entry
	}
	return out
}

// TestQueryEconomicDetailFinding2ZeroShareTargetKeepsCallMarginComplete is the
// exact sixth-pass reproduction at the durable call boundary: a conserved USD 10
// source allocates 0/1 to the in-scope target and 1/1 to a sibling call target
// outside the query. The in-scope target incurs exactly zero attributable cost,
// so it is a canonical known-zero exemption instead of an unresolved block.
func TestQueryEconomicDetailFinding2ZeroShareTargetKeepsCallMarginComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-zero", "USD")
	aLegID := "a-ed-f2-zero"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF2Zero")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF2Zero")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f2-zero",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	siblingCall, _ := edCompleteCall(t, store, account.ID, aLegID, "edF2Sibling")
	query := edFinding3CallQuery(store, account.ID, aLegID, callID.String())
	before, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, before.Margin.Complete)

	record := edFinding2ShareAllocation(t, store, account.ID, "shared-f2-zero", "USD", "10",
		economics.AllocationTarget{TargetID: "t-f2-zero", Target: subject, Weight: edFinding2Weight("0", "1")},
		economics.AllocationTarget{TargetID: "t-f2-sibling", Target: edFinding2CallTarget(store, account.ID, aLegID, siblingCall.String()), Weight: edFinding2Weight("1", "1")},
	)
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, after.Margin.Complete, "an exact zero attributable share cannot block the call margin")
	require.Equal(t, "complete", after.Margin.Reason)
	require.Equal(t, 0, after.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, after.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := after.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, "t-f2-zero", entry.TargetID)
	require.Equal(t, billing.EconomicDetailCostCoverageKnownZero, entry.State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageKnownZero, entry.Reason)
	require.NotNil(t, after.Totals.SelectedAmount)
	require.Equal(t, edTestDecimal(t, "1.32").CanonicalString(), after.Totals.SelectedAmount.Decimal.CanonicalString())
}

// TestQueryEconomicDetailFinding2NonzeroShareSiblingStaysInclusionRequired
// proves the durable classification is per target: the nonzero-share sibling of
// the same conserved source remains a live attributable cost.
func TestQueryEconomicDetailFinding2NonzeroShareSiblingStaysInclusionRequired(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-sibling", "USD")
	aLegID := "a-ed-f2-sibling"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF2Sibling")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF2Sibling")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f2-sibling",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	record := edFinding2ShareAllocation(t, store, account.ID, "shared-f2-sibling", "USD", "10",
		economics.AllocationTarget{TargetID: "t-f2-zero", Target: subject, Weight: edFinding2Weight("0", "1")},
		economics.AllocationTarget{TargetID: "t-f2-nonzero", Target: edFinding2CallTarget(store, account.ID, aLegID, callID.String()), Weight: edFinding2Weight("1", "1")},
	)
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.False(t, after.Margin.Complete)
	require.Equal(t, "allocation_coverage_unresolved", after.Margin.Reason)
	require.Equal(t, 1, after.Coverage.CostCoverage.AllocationUnresolvedCount, "only the nonzero-share target is inclusion-required")
	byTarget := edFinding2CoverageByTarget(t, after)
	require.Equal(t, billing.EconomicDetailCostCoverageKnownZero, byTarget["t-f2-zero"].State)
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, byTarget["t-f2-nonzero"].State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageNotIncluded, byTarget["t-f2-nonzero"].Reason)
}

// TestQueryEconomicDetailALegFinding2ZeroShareTargetKeepsMarginComplete proves
// the same exact-zero semantics at the A-leg scope: the nonzero-share sibling of
// another A-leg is out of scope and the zero-share target cannot block the
// A-leg margin.
func TestQueryEconomicDetailALegFinding2ZeroShareTargetKeepsMarginComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-aleg", "USD")
	aLegID := "a-ed-f2-aleg"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF2ALeg")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF2ALeg")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f2-aleg",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	otherALegID := "a-ed-f2-aleg-other"
	otherCall, _ := edCompleteCall(t, store, account.ID, otherALegID, "edF2ALegOther")
	query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID}
	before, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, before.Margin.Complete)

	record := edFinding2ShareAllocation(t, store, account.ID, "shared-f2-aleg", "USD", "10",
		economics.AllocationTarget{TargetID: "t-f2-aleg-zero", Target: subject, Weight: edFinding2Weight("0", "1")},
		economics.AllocationTarget{TargetID: "t-f2-aleg-other", Target: edFinding2CallTarget(store, account.ID, otherALegID, otherCall.String()), Weight: edFinding2Weight("1", "1")},
	)
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, after.Margin.Complete, "an exact zero attributable share cannot block the A-leg margin")
	require.Equal(t, 0, after.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, after.Coverage.CostCoverage.AllocationCoverage, 1)
	require.Equal(t, "t-f2-aleg-zero", after.Coverage.CostCoverage.AllocationCoverage[0].TargetID)
	require.Equal(t, billing.EconomicDetailCostCoverageKnownZero, after.Coverage.CostCoverage.AllocationCoverage[0].State)
}

// TestQueryEconomicDetailFinding2RoundedZeroNonzeroShareStaysUnresolved is the
// rounding control: a sub-nano source split 1/2 per target floors both targets
// to a zero integer projection (the residual lands on one), yet each exact share
// is nonzero, so neither may be exempted as known zero.
func TestQueryEconomicDetailFinding2RoundedZeroNonzeroShareStaysUnresolved(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-round", "USD")
	aLegID := "a-ed-f2-round"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF2Round")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF2Round")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f2-round",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	record := edFinding2ShareAllocation(t, store, account.ID, "shared-f2-round", "USD", "0.000000001",
		economics.AllocationTarget{TargetID: "t-f2-round-leg", Target: subject, Weight: edFinding2Weight("1", "2")},
		economics.AllocationTarget{TargetID: "t-f2-round-call", Target: edFinding2CallTarget(store, account.ID, aLegID, callID.String()), Weight: edFinding2Weight("1", "2")},
	)
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.False(t, after.Margin.Complete)
	require.Equal(t, 2, after.Coverage.CostCoverage.AllocationUnresolvedCount, "a rounded zero of a nonzero exact share is still real cost")
	for _, entry := range after.Coverage.CostCoverage.AllocationCoverage {
		require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, entry.State)
	}
}

// TestQueryEconomicDetailFinding2RedactedZeroShareDoesNotLeakOrExempt proves an
// account-less source cannot gain a zero exemption from its weight and never
// leaks an amount or currency through coverage.
func TestQueryEconomicDetailFinding2RedactedZeroShareDoesNotLeakOrExempt(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f2-redacted", "USD")
	aLegID := "a-ed-f2-redacted"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF2Redacted")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF2Redacted")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f2-redacted",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	value := edTestDecimal(t, "10")
	// The source is deliberately account-less: the target subjects must therefore
	// carry no account or tenant identity, mirroring the surviving redacted-source
	// fixture, so the reader can keep the membership but not the economics.
	zeroTarget := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store.StoreID(), BillingCallID: callID.String(), BLegID: "b-edF2Redacted"}
	callTarget := metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: store.StoreID(), BillingCallID: callID.String()}
	record := economics.AllocationRecord{
		ID: "alloc-f2-redacted", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: store.StoreID(),
			ResourceID: "shared-f2-redacted", PeriodID: "2026-09",
		},
		SourceBasis: economics.BasisAllocatedCost, SourceAmount: &value, Currency: "USD",
		Policy:        edSupAllocationPolicy(),
		Operation:     economics.AllocationOperationAllocate,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-f2-redacted-zero", Target: zeroTarget, Weight: edFinding2Weight("0", "1")},
			{TargetID: "t-f2-redacted-call", Target: callTarget, Weight: edFinding2Weight("1", "1")},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.False(t, after.Margin.Complete)
	require.NotEmpty(t, after.Coverage.Allocations)
	require.True(t, after.Coverage.AllocationState.Redacted)
	for _, line := range after.Coverage.Allocations {
		require.Nil(t, line.SourceAmount)
		require.Nil(t, line.RoundedAmount)
	}
	byTarget := edFinding2CoverageByTarget(t, after)
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, byTarget["t-f2-redacted-zero"].State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageRedactedSource, byTarget["t-f2-redacted-zero"].Reason)
	require.True(t, byTarget["t-f2-redacted-zero"].Redacted)
	require.Empty(t, byTarget["t-f2-redacted-zero"].Currency)
	require.Equal(t, 2, after.Coverage.CostCoverage.AllocationUnresolvedCount)
}
