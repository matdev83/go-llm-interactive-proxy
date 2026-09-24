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

// Phase 16 fifth-pass Finding 3 durable boundary proof: QueryEconomicDetail must
// make every active attributable monetary allocation participate in selected
// cost completeness. A complete/payable lineage, a matching target B-leg or a
// matching currency never proves inclusion; only the exact frozen selected
// valuation's allocation-coverage reference (identity plus payload hash) does.
// Explicit canonical zero is the only monetary exemption. Allocation amounts are
// never summed into the selected subtotal or margin.

// edFinding3Allocation builds one active positive (or explicit-zero) monetary
// allocation targeting one in-scope B-leg for the given account.
func edFinding3Allocation(t *testing.T, store *DurableStore, accountID, resourceID, currency string, target metering.SubjectRef, amount string) economics.AllocationRecord {
	t.Helper()
	value := edTestDecimal(t, amount)
	return economics.AllocationRecord{
		ID: "alloc-f3-" + resourceID, Version: 1,
		SourceSubject: edSupAllocationSource(store, accountID, resourceID),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &value, Currency: currency,
		Policy:        edSupAllocationPolicy(),
		Operation:     economics.AllocationOperationAllocate,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-" + resourceID, Target: target, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
}

func edFinding3CallQuery(store *DurableStore, accountID, aLegID, callID string) billing.EconomicDetailQuery {
	return billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: accountID, BillingCallID: callID, ALegID: aLegID,
	}
}

// TestQueryEconomicDetailFinding3ActiveAllocationAbsentFromSelectedIncomplete is
// the exact fifth-pass reproduction at the durable call boundary: a normal
// active USD 0.25 allocation targeting the scoped B-leg leaves the selected
// amount and complete/payable state unchanged, so the margin must stay
// incomplete without an explicit inclusion link.
func TestQueryEconomicDetailFinding3ActiveAllocationAbsentFromSelectedIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f3-absent", "USD")
	aLegID := "a-ed-f3-absent"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF3Absent")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF3Absent")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f3-absent",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	query := edFinding3CallQuery(store, account.ID, aLegID, callID.String())
	before, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, before.Margin.Complete, "the control scope is complete before any allocation exists")

	record := edFinding3Allocation(t, store, account.ID, "shared-f3-absent", "USD", subject, "0.25")
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.NotNil(t, after.Coverage.AllocationState)
	require.True(t, after.Coverage.AllocationState.Complete)
	require.True(t, after.Coverage.AllocationState.Payable)
	require.NotNil(t, after.Coverage.CostCoverage)
	require.False(t, after.Coverage.CostCoverage.Complete,
		"a complete/payable allocation absent from the selected amount must not produce complete cost coverage")
	require.Equal(t, 1, after.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, after.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := after.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, record.ID, entry.AllocationID)
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageNotIncluded, entry.Reason)

	require.False(t, after.Margin.Complete, "an allocation omitted from the selected amount cannot support a complete margin")
	require.Nil(t, after.Margin.Amount)
	require.Equal(t, "allocation_coverage_unresolved", after.Margin.Reason)
	// The allocation amount is never summed into the selected subtotal.
	require.NotNil(t, after.Totals.SelectedAmount)
	require.Equal(t, edTestDecimal(t, "1.32").CanonicalString(), after.Totals.SelectedAmount.Decimal.CanonicalString())
}

// TestQueryEconomicDetailALegFinding3ActiveAllocationAbsentIncomplete proves the
// same participation and no-summing at the A-leg scope.
func TestQueryEconomicDetailALegFinding3ActiveAllocationAbsentIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f3-aleg", "USD")
	aLegID := "a-ed-f3-aleg"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF3ALeg")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF3ALeg")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f3-aleg",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	query := billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: account.ID, ALegID: aLegID}
	before, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.True(t, before.Margin.Complete)

	record := edFinding3Allocation(t, store, account.ID, "shared-f3-aleg", "USD", subject, "0.25")
	require.NoError(t, store.AppendAllocation(ctx, record))

	after, err := store.QueryEconomicDetail(ctx, query)
	require.NoError(t, err)
	require.False(t, after.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, after.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.False(t, after.Margin.Complete)
	require.Equal(t, "allocation_coverage_unresolved", after.Margin.Reason)
	require.NotNil(t, after.Totals.SelectedAmount)
	require.Equal(t, edTestDecimal(t, "1.32").CanonicalString(), after.Totals.SelectedAmount.Decimal.CanonicalString())
}

// TestQueryEconomicDetailFinding3CurrencyIncompatibleAllocationIncomplete proves
// a currency-incompatible allocation cannot prove inclusion and reports the
// truthful cause.
func TestQueryEconomicDetailFinding3CurrencyIncompatibleAllocationIncomplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f3-currency", "USD")
	aLegID := "a-ed-f3-currency"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF3Currency")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF3Currency")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f3-currency",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	record := edFinding3Allocation(t, store, account.ID, "shared-f3-currency", "EUR", subject, "0.25")
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageCurrencyMismatch, entry.Reason)
	require.False(t, got.Margin.Complete)
	require.Equal(t, "allocation_coverage_unresolved", got.Margin.Reason)
}

// TestQueryEconomicDetailFinding3RedactedAllocationFailsClosed proves an
// account-less source cannot be silently treated as included or zero and never
// leaks an amount through the coverage projection.
func TestQueryEconomicDetailFinding3RedactedAllocationFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f3-redacted", "USD")
	aLegID := "a-ed-f3-redacted"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF3Redact")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF3Redact")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f3-redacted",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	value := edTestDecimal(t, "0.25")
	record := economics.AllocationRecord{
		ID: "alloc-f3-redacted", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: store.StoreID(),
			ResourceID: "shared-f3-redacted", PeriodID: "2026-09",
		},
		SourceBasis: economics.BasisAllocatedCost, SourceAmount: &value, Currency: "USD",
		Policy:        edSupAllocationPolicy(),
		Operation:     economics.AllocationOperationAllocate,
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-redacted", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store.StoreID(), BillingCallID: callID.String(), BLegID: "b-edF3Redact"}, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.NotEmpty(t, got.Coverage.Allocations)
	require.True(t, got.Coverage.AllocationState.Redacted)
	require.True(t, got.Coverage.Allocations[0].Redacted)
	require.Nil(t, got.Coverage.Allocations[0].SourceAmount)
	require.False(t, got.Coverage.CostCoverage.Complete)
	require.Equal(t, 1, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	entry := got.Coverage.CostCoverage.AllocationCoverage[0]
	require.Equal(t, billing.EconomicDetailCostCoverageUnresolved, entry.State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageRedactedSource, entry.Reason)
	require.True(t, entry.Redacted)
	require.Empty(t, entry.Currency, "a redacted source must not leak an amount or currency through coverage")
	require.False(t, got.Margin.Complete)
	require.Equal(t, "allocation_coverage_unresolved", got.Margin.Reason)
}

// TestQueryEconomicDetailFinding3ExplicitZeroAllocationKeepsMarginComplete
// proves an exact canonical zero allocation is exempt and does not block a
// complete margin.
func TestQueryEconomicDetailFinding3ExplicitZeroAllocationKeepsMarginComplete(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f3-zero", "USD")
	aLegID := "a-ed-f3-zero"
	callID, _ := edCompleteCall(t, store, account.ID, aLegID, "edF3Zero")
	subject := edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, aLegID, callID.String(), "b-edF3Zero")
	edTestPostSelectedHead(t, store, account.ID, callID, subject, "head-ed-f3-zero",
		billing.OperatorCostBasisP, billing.OperatorCostSelectionStatusFinal, billing.OperatorCostProvenanceAttempted, "USD", "1.32")

	record := edFinding3Allocation(t, store, account.ID, "shared-f3-zero", "USD", subject, "0")
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.True(t, got.Margin.Complete)
	require.Equal(t, "complete", got.Margin.Reason)
	require.Equal(t, 0, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	require.Equal(t, billing.EconomicDetailCostCoverageKnownZero, got.Coverage.CostCoverage.AllocationCoverage[0].State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageKnownZero, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
}

// TestQueryEconomicDetailFinding3ProvenIncludedAllocationCompletes proves the
// positive control: an exact selected allocation-coverage reference on the
// frozen selected valuation completes coverage without adding the allocation
// amount to the selected subtotal or margin.
func TestQueryEconomicDetailFinding3ProvenIncludedAllocationCompletes(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-f3-included", "USD")
	aLegID := "a-ed-f3-included"

	callID, subject, moneyCharge, moneyObs, refs := edFinding2Base(t, store, account.ID, aLegID, "edF3Included")
	record := edFinding3Allocation(t, store, account.ID, "shared-f3-included", "USD", subject, "0.25")
	require.NoError(t, store.AppendAllocation(ctx, record))
	ref := edBalanceAllocationRef(store, record)

	frozen := edTestValuationWithReportedLine(t, "val-edF3Included-p", economics.BasisProviderReported, subject, refs, store.StoreID(), moneyCharge, moneyObs, edTestCurrencyTotal(t, "USD", "1.32"))
	frozen.AllocationCoverageRefs = []economics.AllocationRef{ref}
	// Allocation coverage participates in the canonical valuation input
	// identity; refreeze it after binding so the trusted durable boundary
	// accepts the fixture instead of treating the observation-only hash as a
	// stale mismatch.
	frozenHash, err := economics.CanonicalValuationInputSetHash(frozen.Basis, frozen.InputObservations, frozen.AllocationCoverageRefs)
	require.NoError(t, err)
	frozen.InputSetHash = frozenHash
	require.NoError(t, frozen.Validate())
	require.NoError(t, store.AppendValuation(ctx, frozen))
	edTestPostSelectedHeadBound(t, store, account.ID, callID, subject, "head-ed-f3-included",
		billing.SelectedCostValuationRef{ValuationID: frozen.ID, Revision: 1, InputSetHash: frozen.InputSetHash}, "USD", "1.32")

	got, err := store.QueryEconomicDetail(ctx, edFinding3CallQuery(store, account.ID, aLegID, callID.String()))
	require.NoError(t, err)
	require.NotNil(t, got.Coverage.CostCoverage)
	require.True(t, got.Coverage.CostCoverage.Complete, "an exactly proven allocation must not block cost coverage")
	require.True(t, got.Margin.Complete)
	require.Equal(t, "complete", got.Margin.Reason)
	require.NotNil(t, got.Margin.Amount)
	require.Equal(t, edTestDecimal(t, "0.68").CanonicalString(), got.Margin.Amount.Decimal.CanonicalString(), "the allocation amount must not be double counted")
	require.Equal(t, 0, got.Coverage.CostCoverage.AllocationUnresolvedCount)
	require.Len(t, got.Coverage.CostCoverage.AllocationCoverage, 1)
	require.Equal(t, billing.EconomicDetailCostCoverageSelected, got.Coverage.CostCoverage.AllocationCoverage[0].State)
	require.Equal(t, billing.EconomicDetailAllocationCoverageIncluded, got.Coverage.CostCoverage.AllocationCoverage[0].Reason)
	require.NotNil(t, got.Totals.SelectedAmount)
	require.Equal(t, edTestDecimal(t, "1.32").CanonicalString(), got.Totals.SelectedAmount.Decimal.CanonicalString())
}
