//go:build integration

package billingstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// Phase 16 eighth-pass Finding 2 PostgreSQL-direct proofs: the same
// allocation-aware rating -> dependent reconciliation composition as SQLite,
// plus allocation-only replacement following the new head and never the old
// output, on a direct PostgreSQL store.
func TestPhase16Finding2PostgresAllocationRatingPromotesReconciliation(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	observation := phase4EconomicsObservation("test", "finding2-pg-promote", 81)
	allocV1, _ := seventhPassReplacementAllocations(store.StoreID())
	headKey := "finding2-pg-promote-head"
	rating := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_320_000, 0).UTC())
	dependency := phase16Finding2SQLiteDependency(t, rating)
	ratingID, err := rating.Identity()
	require.NoError(t, err)
	require.NotEmpty(t, ratingID.DerivationHash)

	reconciliation := phase16Finding2SQLiteReconciliation(t, "test", dependency)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, rating))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))

	checks, err := store.EconomicRevisionDependencyChecks(ctx, reconciliation)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	require.Equal(t, billing.EconomicJobDependencyMissing, checks[0].Status)

	rater := &seventhPassEchoAllocationRater{}
	reconciler := &economicJobRunnerStoreReconciler{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
	require.NoError(t, err)
	first, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, first.Completed)

	valuation, err := store.LoadEconomicRevisionDependencyOutput(ctx, dependency)
	require.NoError(t, err)
	require.Equal(t, ratingID.ValuationKey(), valuation.ID)
	require.Equal(t, []economics.AllocationRef{allocV1}, valuation.AllocationCoverageRefs)

	second, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, second.Completed)
	require.Len(t, reconciler.calls, 1)
	require.Equal(t, ratingID.ValuationKey(), reconciler.calls[0].Valuation.ID)

	identity, err := reconciliation.Identity()
	require.NoError(t, err)
	probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
	require.NoError(t, err)
	require.True(t, probed)

	third, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Zero(t, third.Claimed)
	require.Equal(t, 1, rater.calls)
}

func TestPhase16Finding2PostgresReplacementFollowsNewHeadNeverOld(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	observation := phase4EconomicsObservation("test", "finding2-pg-replacement", 82)
	allocV1, allocV2 := seventhPassReplacementAllocations(store.StoreID())
	headKey := "finding2-pg-replacement-head"
	first := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_321_000, 0).UTC())
	second := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV2}, headKey, time.Unix(1_700_321_100, 0).UTC())
	firstDep := phase16Finding2SQLiteDependency(t, first)
	secondDep := phase16Finding2SQLiteDependency(t, second)
	require.NotEqual(t, firstDep.Key(), secondDep.Key())
	firstID, err := first.Identity()
	require.NoError(t, err)
	secondID, err := second.Identity()
	require.NoError(t, err)

	reconciliation := phase16Finding2SQLiteReconciliation(t, "test", secondDep)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))

	rater := &seventhPassEchoAllocationRater{}
	reconciler := &economicJobRunnerStoreReconciler{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
	require.NoError(t, err)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, summary.Completed)

	loaded, err := store.LoadEconomicRevisionDependencyOutput(ctx, secondDep)
	require.NoError(t, err)
	require.Equal(t, secondID.ValuationKey(), loaded.ID)

	promoted, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, promoted.Completed)
	require.Len(t, reconciler.calls, 1)
	require.Equal(t, secondID.ValuationKey(), reconciler.calls[0].Valuation.ID)

	firstLoaded, err := store.LoadEconomicRevisionDependencyOutput(ctx, firstDep)
	require.NoError(t, err)
	require.Equal(t, firstID.ValuationKey(), firstLoaded.ID)
	require.NotEqual(t, firstLoaded.ID, reconciler.calls[0].Valuation.ID)
}
