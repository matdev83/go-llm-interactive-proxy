//go:build integration

package billingstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// TestEconomicJobRunnerPostgresDirect proves the revision-aware economic job
// application orchestration and its reconciliation output adapter on direct
// PostgreSQL: exact dependency output loading, idempotent reconciliation
// replay/conflict and rating-then-reconciliation dependency promotion.
func TestEconomicJobRunnerPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	t.Run("dependency output load", func(t *testing.T) {
		rating := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 1, "pg-dependency")
		dependency := economicJobRunnerStoreDependency(t, rating)
		_, err := store.LoadEconomicRevisionDependencyOutput(ctx, dependency)
		require.ErrorIs(t, err, billing.ErrEconomicRevisionDependencyOutputMissing)

		require.NoError(t, store.AppendEconomicRevisionResult(ctx, rating, billing.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, rating)}))
		valuation, err := store.LoadEconomicRevisionDependencyOutput(ctx, dependency)
		require.NoError(t, err)
		require.Equal(t, dependency.InputSetHash, valuation.InputSetHash)
		require.Equal(t, economics.ValuationVersionV2, valuation.Version)
	})

	t.Run("reconciliation output replay and conflict", func(t *testing.T) {
		rating := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 1, "pg-reconciliation-dependency")
		work := economicJobRunnerStoreReconciliation(t, rating)
		normalizedWork, err := work.Normalize()
		require.NoError(t, err)
		identity, err := normalizedWork.Identity()
		require.NoError(t, err)
		record := billing.EconomicReconciliation{
			ID: identity.ReconciliationKey(), Version: identity.EvidenceRevision,
			Subject: normalizedWork.Subject, Scope: normalizedWork.Input.Scope, Basis: normalizedWork.Input.Basis,
			InputSetHash: normalizedWork.InputSetHash, ResultJSON: json.RawMessage(`{"status":"matched"}`),
			CreatedAt: time.Unix(1_700_110_000, 0).UTC(),
		}
		require.NoError(t, store.AppendEconomicRevisionReconciliation(ctx, work, record))
		require.NoError(t, store.AppendEconomicRevisionReconciliation(ctx, work, record))
		probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
		require.NoError(t, err)
		require.True(t, probed)

		conflicting := record
		conflicting.ResultJSON = json.RawMessage(`{"status":"conflict"}`)
		err = store.AppendEconomicRevisionReconciliation(ctx, work, conflicting)
		require.ErrorIs(t, err, billing.ErrEconomicRevisionConflict)

		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ?`,
			"test", identity.ReconciliationKey(), int64(identity.EvidenceRevision)).Scan(ctx, &count))
		require.Equal(t, 1, count)
	})

	t.Run("runner promotes reconciliation", func(t *testing.T) {
		rating := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 1, "pg-runner-rating")
		reconciliation := economicJobRunnerStoreReconciliation(t, rating)
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, rating))
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))
		rater := &refinement42Rater{}
		reconciler := &economicJobRunnerStoreReconciler{}
		runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
		require.NoError(t, err)

		before, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
		require.NoError(t, err)
		require.Equal(t, 1, before.IncompleteDependencies)

		first, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
		require.NoError(t, err)
		require.Equal(t, 1, first.Completed)
		require.Equal(t, 1, first.Backlog.Pending)
		require.Zero(t, first.Backlog.IncompleteDependencies)
		require.Equal(t, 1, rater.calls)

		second, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
		require.NoError(t, err)
		require.Equal(t, 1, second.Completed)
		require.Zero(t, second.Backlog.Pending)
		require.Zero(t, second.Backlog.IncompleteDependencies)
		require.Len(t, reconciler.calls, 1)

		identity, err := reconciliation.Identity()
		require.NoError(t, err)
		probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
		require.NoError(t, err)
		require.True(t, probed)
	})
}
