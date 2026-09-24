package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 second-pass Finding 5 core contract: the retained aggregate
// reconciliation plane is an independent typed plane. It must survive
// assembly verbatim, participate in full-scope margin completeness, and never
// be coerced into the quantity or monetary planes or used as a monetary amount.

func detailTestAggregateAmount(t *testing.T, currency, amount string) *MonetaryExactAmount {
	t.Helper()
	value := detailTestDecimal(t, amount)
	return &MonetaryExactAmount{Currency: currency, Decimal: &value}
}

// detailTestAggregatePlane builds one bounded aggregate plane with a single
// row carrying the requested classification and (for non-comparable
// classifications) the retained missing/incomparable/conflict evidence identity.
func detailTestAggregatePlane(t *testing.T, status ReconciliationComparisonStatus, ids ...string) ReconciliationAggregate {
	t.Helper()
	row := ReconciliationAggregateRow{
		Scope:                         "call:aggregate-test",
		Currency:                      "USD",
		GrossAbsoluteDiscrepancy:      detailTestAggregateAmount(t, "USD", "0.00"),
		DiscrepantAbsoluteDiscrepancy: detailTestAggregateAmount(t, "USD", "0.00"),
		NetSignedDiscrepancy:          detailTestAggregateAmount(t, "USD", "0.00"),
		StatusCounts:                  []ReconciliationStatusCount{{Status: status, Count: 1}},
	}
	switch status {
	case ReconciliationStatusMissingProvider, ReconciliationStatusMissingLocal:
		row.MissingIDs = append([]string(nil), ids...)
	case ReconciliationStatusIncomparable:
		row.IncomparableIDs = append([]string(nil), ids...)
	case ReconciliationStatusConflict:
		row.ConflictIDs = append([]string(nil), ids...)
	}
	return ReconciliationAggregate{
		Policy: VersionRef{ID: "policy-detail", Version: "v1"},
		Rows:   []ReconciliationAggregateRow{row},
	}
}

// TestAssembleEconomicDetailAggregateOnlyPlaneComplete proves a complete
// aggregate-only reconciliation identity is a valid, truthful plane: it does
// not invent singular quantity/monetary projections, it satisfies full-scope
// reconciliation completeness, and the margin amount stays anchored to the
// selected/retail planes rather than the aggregate.
func TestAssembleEconomicDetailAggregateOnlyPlaneComplete(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-agg-complete")

	in := detailTestCompleteInput(t)
	in.Quantity, in.Monetary = nil, nil
	aggregate := detailTestAggregatePlane(t, ReconciliationStatusMatched)
	in.Reconciliations = []EconomicDetailReconciliation{{Subject: subject, Aggregate: &aggregate}}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 1)
	require.NotNil(t, got.Reconciliations[0].Aggregate, "the aggregate plane must survive assembly")
	require.Nil(t, got.Reconciliations[0].Quantity)
	require.Nil(t, got.Reconciliations[0].Monetary)
	require.Nil(t, got.Quantity, "an aggregate-only identity must not invent a singular quantity plane")
	require.Nil(t, got.Monetary, "an aggregate-only identity must not invent a singular monetary plane")
	require.True(t, got.Margin.Complete, "a complete aggregate plane can satisfy reconciliation completeness")
	require.NotNil(t, got.Margin.Amount, "the margin amount is anchored to the selected/retail planes, not the aggregate")
}

// TestAssembleEconomicDetailIncompleteAggregateBlocksMargin proves an
// incomplete aggregate classification (missing/incomparable/conflict evidence)
// fails closed for a complete margin even when the aggregate is the only plane.
func TestAssembleEconomicDetailIncompleteAggregateBlocksMargin(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()

	for _, tc := range []struct {
		name   string
		status ReconciliationComparisonStatus
		id     string
	}{
		{name: "missing evidence", status: ReconciliationStatusMissingProvider, id: "end-to-end:USD"},
		{name: "incomparable evidence", status: ReconciliationStatusIncomparable, id: "end-to-end:USD"},
		{name: "conflicting evidence", status: ReconciliationStatusConflict, id: "end-to-end:USD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			subject := detailTestSubject(storeID, aLegID, billingCallID, "b-agg-incomplete")
			in := detailTestCompleteInput(t)
			in.Quantity, in.Monetary = nil, nil
			aggregate := detailTestAggregatePlane(t, tc.status, tc.id)
			in.Reconciliations = []EconomicDetailReconciliation{{Subject: subject, Aggregate: &aggregate}}

			got, err := AssembleEconomicDetail(in)
			require.NoError(t, err)
			require.False(t, got.Margin.Complete, "an incomplete aggregate plane cannot support a complete margin")
			require.Nil(t, got.Margin.Amount)
			require.Equal(t, "comparison_incomparable", got.Margin.Reason)
		})
	}
}

// TestAssembleEconomicDetailReconciliationPreservesAllPlanes proves mixed
// quantity, monetary and aggregate planes of one identity are all preserved
// independently and never collapse into one another.
func TestAssembleEconomicDetailReconciliationPreservesAllPlanes(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-agg-mixed")

	entry := detailTestReconciliation(t, storeID, subject, ReconciliationStatusDiscrepant)
	aggregate := detailTestAggregatePlane(t, ReconciliationStatusDiscrepant)
	entry.Aggregate = &aggregate

	in := detailTestCompleteInput(t)
	in.Quantity, in.Monetary = nil, nil
	in.Reconciliations = []EconomicDetailReconciliation{entry}

	got, err := AssembleEconomicDetail(in)
	require.NoError(t, err)
	require.Len(t, got.Reconciliations, 1)
	require.NotNil(t, got.Reconciliations[0].Quantity)
	require.NotNil(t, got.Reconciliations[0].Monetary)
	require.NotNil(t, got.Reconciliations[0].Aggregate)
	require.NotNil(t, got.Quantity, "the singular convenience projection still follows the quantity plane")
	require.True(t, got.Margin.Complete)
}

// TestAssembleEconomicDetailAggregateForeignStoreFailsClosed proves an
// aggregate plane whose finding evidence belongs to another store fails closed
// rather than leaking foreign supplier evidence into the requested scope.
func TestAssembleEconomicDetailAggregateForeignStoreFailsClosed(t *testing.T) {
	t.Parallel()
	storeID, _, aLegID, billingCallID := detailTestScope()
	subject := detailTestSubject(storeID, aLegID, billingCallID, "b-agg-foreign")

	aggregate := detailTestAggregatePlane(t, ReconciliationStatusMatched)
	aggregate.Findings = []ReconciliationFinding{{
		ID: "foreign-finding", Scope: "call:aggregate-test",
		Status: ReconciliationStatusMatched, EvaluatedStatus: ReconciliationStatusMatched,
		SourceObservationRefs: []metering.ObservationRef{{
			StoreID: "foreign-store", ObservationID: "obs", Revision: 1, PayloadHash: strings.Repeat("a", 64),
		}},
	}}

	in := detailTestCompleteInput(t)
	in.Quantity, in.Monetary = nil, nil
	in.Reconciliations = []EconomicDetailReconciliation{{Subject: subject, Aggregate: &aggregate}}

	_, err := AssembleEconomicDetail(in)
	require.ErrorIs(t, err, ErrEconomicDetailScopeMismatch)
}
