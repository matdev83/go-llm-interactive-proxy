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

// Phase 16 second-pass Finding 5 durable boundary contract: a canonical
// aggregate-only retained reconciliation must not fail the whole discrepancy
// page, and a retained aggregate plane must survive the durable readers with
// its bounded classification and missing-evidence identity intact.

// orTestAggregateOnlyRetention builds a canonical aggregate-only retention
// result (no quantity or monetary plane) whose projection carries no findings.
func orTestAggregateOnlyRetention(t *testing.T, storeID, id string, revision uint64, subject metering.SubjectRef, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	policy := billing.VersionRef{ID: "policy-agg-or", Version: "v1"}
	return billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision, Subject: subject, Scope: "call:or",
		Policy:    policy,
		Aggregate: &billing.ReconciliationAggregate{Policy: policy},
		CreatedAt: createdAt,
	}
}

// orTestMonetaryAggregateRetention builds a canonical retention result whose
// authoritative monetary plane is missing P and whose aggregate plane therefore
// retains missing-provider evidence. The monetary plane is the required source
// binding for the aggregate findings, so the result stays canonical.
func orTestMonetaryAggregateRetention(t *testing.T, storeID, id string, revision uint64, subject metering.SubjectRef, createdAt time.Time) billing.ReconciliationRetentionResult {
	t.Helper()
	expected := orTestValuationForSubject(t, storeID, id+"-e", economics.BasisLocalExpected, "1.00", subject)
	quantityLocal := orTestValuationForSubject(t, storeID, id+"-q", economics.BasisProviderQuantityLocal, "1.10", subject)
	monetary, err := billing.DecomposeMonetaryDiscrepancies(billing.MonetaryDiscrepancyInput{Valuations: []economics.Valuation{expected, quantityLocal}})
	require.NoError(t, err)
	findings, err := billing.ReconciliationFindingsFromMonetaryComparison("call:or", monetary)
	require.NoError(t, err)
	limit := metering.Decimal{Coefficient: "4", Scale: 1}
	policy := billing.ReconciliationTolerancePolicy{
		Version: billing.ReconciliationTolerancePolicyV1,
		Ref:     billing.VersionRef{ID: "policy-agg-or", Version: "v1"},
		Rules: []billing.ReconciliationToleranceRule{{
			ID: "usd", Scope: billing.ReconciliationToleranceScope{Currency: "USD"}, AbsoluteLimit: &limit,
		}},
	}
	aggregate, err := billing.AggregateReconciliationFindings(policy, findings)
	require.NoError(t, err)
	return billing.ReconciliationRetentionResult{
		SchemaVersion: billing.ReconciliationRetentionSchemaVersionV1,
		ID:            id, ResultRevision: revision, Subject: subject, Scope: "call:or",
		Policy:    policy.Ref,
		Monetary:  &monetary,
		Aggregate: &aggregate,
		CreatedAt: createdAt,
	}
}

// TestDiscrepancyViewFromRetentionAggregateOnly is the direct behavioral proof
// that a canonical aggregate-only result currently cannot be projected into a
// valid DiscrepancyView. It uses only the existing contracts so the failure is
// behavioral rather than a missing-symbol build failure.
func TestDiscrepancyViewFromRetentionAggregateOnly(t *testing.T) {
	t.Parallel()
	subject := orTestRetentionSubjectWithAccount("test", "or-agg-account")
	result := orTestAggregateOnlyRetention(t, "test", "or-agg-mapper", 1, subject, time.Unix(1_700_030_250, 0).UTC())
	view, err := discrepancyViewFromRetention(result)
	require.NoError(t, err, "a canonical aggregate-only result must project into a valid view")
	require.Equal(t, "or-agg-mapper", view.ID)
}

func TestQueryDiscrepanciesAggregateOnlyResultDoesNotFailPage(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	subject := orTestRetentionSubjectWithAccount(store.StoreID(), "or-agg-account")
	for i, id := range []string{"or-agg-only-1", "or-agg-only-2"} {
		result := orTestAggregateOnlyRetention(t, store.StoreID(), id, 1, subject, time.Unix(1_700_030_200+int64(i), 0).UTC())
		require.NoError(t, store.AppendReconciliationRetention(ctx, result))
	}

	scope := economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-agg-account"}
	first, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{Scope: scope, Limit: 1})
	require.NoError(t, err, "a canonical aggregate-only persisted result must not fail the whole page")
	require.NoError(t, first.Validate())
	require.Len(t, first.Items, 1)
	require.Empty(t, first.Items[0].QuantityStatus)
	require.Empty(t, first.Items[0].MonetaryState)
	require.NotNil(t, first.Items[0].Aggregate)
	require.True(t, first.Items[0].Aggregate.Complete)
	require.NotEmpty(t, first.NextCursor, "pagination must keep working across aggregate-only rows")

	second, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{Scope: scope, Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Empty(t, second.NextCursor)
	require.NotEqual(t, first.Items[0].ID, second.Items[0].ID)
}

func TestQueryDiscrepanciesAggregatePlaneSurvives(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	result := orTestMonetaryAggregateRetention(t, store.StoreID(), "or-agg-detail", 1, orTestRetentionSubject(store.StoreID()), time.Unix(1_700_030_300, 0).UTC())
	require.NoError(t, store.AppendReconciliationRetention(ctx, result))

	page, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), TenantID: "tenant-or"}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	view := page.Items[0]
	require.NotNil(t, view.Aggregate, "the retained aggregate plane must survive the durable reader")
	require.Equal(t, economics.DiscrepancyMissingProvider, view.Aggregate.Status)
	require.Equal(t, []string{"end-to-end:USD"}, view.Aggregate.MissingIDs)
	require.False(t, view.Aggregate.Complete)
	require.NotEmpty(t, view.MonetaryState, "aggregate retention must not collapse the independent monetary plane")
}

func TestQueryDiscrepanciesAggregateOnlyCursorBindsAccount(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	subject := orTestRetentionSubjectWithAccount(store.StoreID(), "or-agg-cursor")
	require.NoError(t, store.AppendReconciliationRetention(ctx,
		orTestAggregateOnlyRetention(t, store.StoreID(), "or-agg-cursor-1", 1, subject, time.Unix(1_700_030_400, 0).UTC())))
	require.NoError(t, store.AppendReconciliationRetention(ctx,
		orTestAggregateOnlyRetention(t, store.StoreID(), "or-agg-cursor-2", 1, subject, time.Unix(1_700_030_401, 0).UTC())))

	first, err := store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-agg-cursor"}, Limit: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)

	// The aggregate plane must not weaken the authenticated cursor binding:
	// replaying it under a foreign account scope fails closed.
	_, err = store.QueryDiscrepancies(ctx, economics.DiscrepancyQuery{
		Scope: economics.OperatorScope{StoreID: store.StoreID(), AccountID: "or-agg-foreign"}, Limit: 1, Cursor: first.NextCursor,
	})
	require.ErrorIs(t, err, economics.ErrOperatorCursorInvalid)
}
