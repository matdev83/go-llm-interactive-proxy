//go:build integration

package billingstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestEconomicJobQueuePostgresDirect proves the revision-aware economic job
// queue on direct PostgreSQL with the same bounded batch claim, lease/fence,
// heartbeat, retry reason, terminal fail, backlog and incomplete-dependency
// behavior as SQLite.
func TestEconomicJobQueuePostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	require.NoError(t, VerifySchema(ctx, store.db))

	t.Run("schema catalog", func(t *testing.T) {
		require.Contains(t, RequiredMigrationNames, BillingEconomicJobQueueMigrationName)
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, BillingEconomicJobQueueMigrationName).Scan(ctx, &count))
		require.Equal(t, 1, count)
		for _, column := range []string{"work_kind", "dependency_count", "retry_reason", "failed_at_unix"} {
			require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'billing_economic_revision_work_state' AND column_name = ?`, column).Scan(ctx, &count), column)
			require.Equal(t, 1, count, column)
		}
		var definition string
		require.NoError(t, store.db.NewRaw(`SELECT pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace WHERE n.nspname = current_schema() AND t.relname = 'billing_economic_revision_work_state' AND c.contype = 'c' AND c.conname = ?`, billingEconomicRevisionWorkStateStatusConstraint).Scan(ctx, &definition))
		require.Contains(t, definition, "failed")
	})

	t.Run("bounded claim batch, lease expiry and stale rejection", func(t *testing.T) {
		work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "pg-fencing")
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
		claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		blocked, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
		require.NoError(t, err)
		require.Empty(t, blocked)

		identity, err := claims[0].Work.Identity()
		require.NoError(t, err)
		_, err = store.db.NewRaw(`UPDATE billing_economic_revision_work_state SET lease_until_unix = 0 WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Exec(ctx)
		require.NoError(t, err)
		recovered, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
		require.NoError(t, err)
		require.Len(t, recovered, 1)
		require.Greater(t, recovered[0].Claim.Fence, claims[0].Claim.Fence)
		require.ErrorIs(t, store.CompleteEconomicRevisionWork(ctx, claims[0].Work, claims[0].Claim), billing.ErrEconomicRevisionClaimLost)
		require.ErrorIs(t, store.FailEconomicRevisionWork(ctx, claims[0].Work, claims[0].Claim, billing.EconomicWorkReasonPermanentFailure), billing.ErrEconomicRevisionClaimLost)

		extended, err := store.HeartbeatEconomicRevisionWork(ctx, recovered[0].Work, recovered[0].Claim, 5*time.Minute)
		require.NoError(t, err)
		require.True(t, extended.LeaseUntil.After(recovered[0].Claim.LeaseUntil))
		require.NoError(t, store.FailEconomicRevisionWork(ctx, recovered[0].Work, extended, billing.EconomicWorkReasonPermanentFailure))
		backlog, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
		require.NoError(t, err)
		require.Equal(t, 1, backlog.Failed)
	})

	t.Run("retry reason and next attempt", func(t *testing.T) {
		work := economicJobSQLiteWork(t, billing.EconomicQueueCustomer, 1, "pg-retry")
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
		claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, "worker-a", time.Minute, 1)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		identity, err := claims[0].Work.Identity()
		require.NoError(t, err)
		due := time.Now().UTC().Add(time.Hour)
		require.NoError(t, store.RetryEconomicRevisionWorkWithReason(ctx, claims[0].Work, claims[0].Claim, billing.EconomicWorkReasonRaterFailure, due))
		var status, retryReason string
		var nextAttempt int64
		require.NoError(t, store.db.NewRaw(`SELECT status, retry_reason, next_attempt_at_unix FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status, &retryReason, &nextAttempt))
		require.Equal(t, string(billing.EconomicWorkStatusPending), status)
		require.Equal(t, string(billing.EconomicWorkReasonRaterFailure), retryReason)
		require.Equal(t, due.UnixNano(), nextAttempt)
		none, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, "worker-b", time.Minute, 8)
		require.NoError(t, err)
		require.Empty(t, none)
	})

	t.Run("incomplete dependencies block claim until rated", func(t *testing.T) {
		rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 2, "pg-dependency-rating")
		reconciliation := economicJobSQLiteReconciliation(t, economicJobSQLiteDependency(t, rating))
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, rating))
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))

		checks, err := store.EconomicRevisionDependencyChecks(ctx, reconciliation)
		require.NoError(t, err)
		require.Len(t, checks, 1)
		require.Equal(t, billing.EconomicJobDependencyMissing, checks[0].Status)
		claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 8)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, billing.EconomicWorkKindProviderRating, claims[0].Work.Kind)
		backlog, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
		require.NoError(t, err)
		require.Equal(t, 1, backlog.IncompleteDependencies)

		require.NoError(t, store.AppendEconomicRevisionResult(ctx, rating, billing.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, rating)}))
		checks, err = store.EconomicRevisionDependencyChecks(ctx, reconciliation)
		require.NoError(t, err)
		require.Equal(t, billing.EconomicJobDependencySatisfied, checks[0].Status)
		reconciliationClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
		require.NoError(t, err)
		require.Len(t, reconciliationClaims, 1)
		require.Equal(t, billing.EconomicWorkKindReconciliation, reconciliationClaims[0].Work.Kind)
	})

	t.Run("concurrent batch claims have one winner per revision", func(t *testing.T) {
		works := make([]billing.EconomicRevisionWork, 0, 6)
		for i := 1; i <= 6; i++ {
			work := economicJobSQLiteWork(t, billing.EconomicQueueCustomer, uint64(i), "pg-concurrent")
			work.HeadKey = "economic-job-head-pg-concurrent"
			work.CreatedAt = time.Unix(1_700_080_000+int64(i), 0).UTC()
			require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
			works = append(works, work)
		}
		type result struct {
			claims []billing.EconomicRevisionClaimedWork
			err    error
		}
		results := make(chan result, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(owner string) {
				defer wg.Done()
				claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, owner, time.Minute, 8)
				results <- result{claims: claims, err: err}
			}("worker-" + string(rune('a'+i)))
		}
		wg.Wait()
		close(results)
		seen := map[string]bool{}
		claimed := 0
		for outcome := range results {
			require.NoError(t, outcome.err)
			for _, claim := range outcome.claims {
				identity, err := claim.Work.Identity()
				require.NoError(t, err)
				require.False(t, seen[identity.Key()], "duplicate concurrent claim")
				seen[identity.Key()] = true
				claimed++
			}
		}
		require.Equal(t, len(works), claimed)
	})
}
