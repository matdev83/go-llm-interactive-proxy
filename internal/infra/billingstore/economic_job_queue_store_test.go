package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func economicJobSQLiteWork(t *testing.T, queue billing.EconomicQueue, revision uint64, quantity string) billing.EconomicRevisionWork {
	t.Helper()
	observation := phase4EconomicsObservation("test", "job-"+queue.String()+"-"+quantity, revision)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	return billing.EconomicRevisionWork{
		Queue: queue, HeadKey: "economic-job-head-" + quantity,
		Subject: observation.Subject, EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_060_000+int64(revision), 0).UTC(),
	}
}

func economicJobSQLiteReconciliation(t *testing.T, deps ...billing.EconomicJobDependency) billing.EconomicRevisionWork {
	t.Helper()
	observation := phase4EconomicsObservation("test", "job-reconciliation-evidence", 7)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: "economic-job-head-reconciliation", Subject: observation.Subject,
		EvidenceRevision: 7, Input: input, Dependencies: deps,
		CreatedAt: time.Unix(1_700_060_500, 0).UTC(),
	}
}

func economicJobSQLiteDependency(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	return billing.EconomicJobDependency{
		Kind: billing.EconomicWorkKindForQueue(normalized.Queue), Queue: normalized.Queue,
		HeadKey: normalized.HeadKey, EvidenceRevision: identity.EvidenceRevision, InputSetHash: identity.InputSetHash,
	}
}

func TestEconomicJobQueueSQLiteSeparatesCustomerAndProviderClaims(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	customer := economicJobSQLiteWork(t, billing.EconomicQueueCustomer, 1, "customer")
	provider := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "provider")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, customer))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, provider))

	customerClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, "customer-worker", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, customerClaims, 1)
	require.Equal(t, billing.EconomicQueueCustomer, customerClaims[0].Work.Queue)
	require.Equal(t, billing.EconomicWorkKindCustomerRating, customerClaims[0].Work.Kind)
	require.NotZero(t, customerClaims[0].Claim.Fence)

	providerClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "provider-worker", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, providerClaims, 1)
	require.Equal(t, billing.EconomicQueueProvider, providerClaims[0].Work.Queue)
	require.Equal(t, billing.EconomicWorkKindProviderRating, providerClaims[0].Work.Kind)
}

func TestEconomicJobQueueSQLiteExactReplayAndChangedRevision(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "replay")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	var workCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ?`, "test").Scan(ctx, &workCount))
	require.Equal(t, 1, workCount)

	// A changed input hash at the same head/revision is a distinct actionable
	// revision, and a later revision is another distinct actionable revision.
	changedInput := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "replay-changed")
	changedInput.HeadKey = work.HeadKey
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, changedInput))
	revisionTwo := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 2, "replay")
	revisionTwo.HeadKey = work.HeadKey
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, revisionTwo))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ?`, "test").Scan(ctx, &workCount))
	require.Equal(t, 3, workCount)

	claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, claims, 3)
	seen := map[string]bool{}
	for _, claim := range claims {
		identity, err := claim.Work.Identity()
		require.NoError(t, err)
		require.False(t, seen[identity.Key()], "duplicate actionable revision")
		seen[identity.Key()] = true
	}
}

func TestEconomicJobQueueSQLiteBoundedClaimBatchAndFencing(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, uint64(i), "batch")
		work.HeadKey = "economic-job-head-batch"
		work.CreatedAt = time.Unix(1_700_070_000+int64(i), 0).UTC()
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	}

	first, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, uint64(1), first[0].Claim.Fence)

	// Live claims are invisible to another bounded batch.
	rest, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, rest, 3)

	// An interrupted worker leaves an expired lease: the work becomes
	// claimable again with a strictly newer fence, and the stale claim can no
	// longer complete, heartbeat, retry or fail the work.
	identity, err := first[0].Work.Identity()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_economic_revision_work_state SET lease_until_unix = 0 WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Exec(ctx)
	require.NoError(t, err)
	recovered, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	require.Greater(t, recovered[0].Claim.Fence, first[0].Claim.Fence)
	require.ErrorIs(t, store.CompleteEconomicRevisionWork(ctx, first[0].Work, first[0].Claim), billing.ErrEconomicRevisionClaimLost)
	_, err = store.HeartbeatEconomicRevisionWork(ctx, first[0].Work, first[0].Claim, time.Minute)
	require.ErrorIs(t, err, billing.ErrEconomicRevisionClaimLost)
	require.ErrorIs(t, store.RetryEconomicRevisionWorkWithReason(ctx, first[0].Work, first[0].Claim, billing.EconomicWorkReasonTransientFailure, time.Time{}), billing.ErrEconomicRevisionClaimLost)
	require.ErrorIs(t, store.FailEconomicRevisionWork(ctx, first[0].Work, first[0].Claim, billing.EconomicWorkReasonPermanentFailure), billing.ErrEconomicRevisionClaimLost)
	require.NoError(t, store.CompleteEconomicRevisionWork(ctx, recovered[0].Work, recovered[0].Claim))

	_, err = store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-c", time.Minute, billing.MaxEconomicRevisionClaimBatchSize+1)
	require.ErrorIs(t, err, billing.ErrInvalidEconomicRevision)
}

func TestEconomicJobQueueSQLiteHeartbeatExtendsLeaseAndRejectsStale(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "heartbeat")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	claim := claims[0].Claim

	extended, err := store.HeartbeatEconomicRevisionWork(ctx, claims[0].Work, claim, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, claim.Fence, extended.Fence)
	require.Equal(t, claim.Owner, extended.Owner)
	require.True(t, extended.LeaseUntil.After(claim.LeaseUntil))

	stale := claim
	stale.Owner = "worker-b"
	_, err = store.HeartbeatEconomicRevisionWork(ctx, claims[0].Work, stale, time.Minute)
	require.ErrorIs(t, err, billing.ErrEconomicRevisionClaimLost)

	require.NoError(t, store.CompleteEconomicRevisionWork(ctx, claims[0].Work, extended))
	var status string
	identity, err := claims[0].Work.Identity()
	require.NoError(t, err)
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status))
	require.Equal(t, "completed", status)
}

func TestEconomicJobQueueSQLiteRetryReasonAndNextAttempt(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "retry-reason")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	identity, err := claims[0].Work.Identity()
	require.NoError(t, err)

	due := time.Now().UTC().Add(time.Hour)
	require.NoError(t, store.RetryEconomicRevisionWorkWithReason(ctx, claims[0].Work, claims[0].Claim, billing.EconomicWorkReasonPersistenceFailure, due))
	var status, retryReason string
	var nextAttempt int64
	require.NoError(t, store.db.NewRaw(`SELECT status, retry_reason, next_attempt_at_unix FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status, &retryReason, &nextAttempt))
	require.Equal(t, string(billing.EconomicWorkStatusPending), status)
	require.Equal(t, string(billing.EconomicWorkReasonPersistenceFailure), retryReason)
	require.Equal(t, due.UnixNano(), nextAttempt)

	// Backoff hides the retry from the bounded claim batch until due.
	none, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
	require.NoError(t, err)
	require.Empty(t, none)
	_, err = store.db.NewRaw(`UPDATE billing_economic_revision_work_state SET next_attempt_at_unix = 0 WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Exec(ctx)
	require.NoError(t, err)
	recovered, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	require.Greater(t, recovered[0].Claim.Fence, claims[0].Claim.Fence)

	// The legacy free-text retry path keeps the bounded closed reason and
	// truncates unbounded error text.
	longReason := strings.Repeat("e", billing.MaxEconomicWorkReasonLength*3)
	require.NoError(t, store.RetryEconomicRevisionWork(ctx, recovered[0].Work, recovered[0].Claim, longReason, time.Time{}))
	var lastError string
	require.NoError(t, store.db.NewRaw(`SELECT retry_reason, last_error FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &retryReason, &lastError))
	require.Equal(t, string(billing.EconomicWorkReasonUnclassified), retryReason)
	require.Len(t, []rune(lastError), billing.MaxEconomicWorkReasonLength)

	err = store.RetryEconomicRevisionWorkWithReason(ctx, recovered[0].Work, recovered[0].Claim, billing.EconomicWorkReason("mystery"), time.Time{})
	require.ErrorIs(t, err, billing.ErrInvalidEconomicRevision)
}

func TestEconomicJobQueueSQLiteFailIsTerminalAndBounded(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueCustomer, 1, "fail")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	identity, err := claims[0].Work.Identity()
	require.NoError(t, err)
	require.NoError(t, store.FailEconomicRevisionWork(ctx, claims[0].Work, claims[0].Claim, billing.EconomicWorkReasonPermanentFailure))

	var status, retryReason string
	var failedAt int64
	require.NoError(t, store.db.NewRaw(`SELECT status, retry_reason, failed_at_unix FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status, &retryReason, &failedAt))
	require.Equal(t, string(billing.EconomicWorkStatusFailed), status)
	require.Equal(t, string(billing.EconomicWorkReasonPermanentFailure), retryReason)
	require.Greater(t, failedAt, int64(0))

	again, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, "worker-b", time.Minute, 8)
	require.NoError(t, err)
	require.Empty(t, again)
	pending, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 8)
	require.NoError(t, err)
	require.Empty(t, pending)

	backlog, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, backlog.Failed)
	require.Zero(t, backlog.Pending)
}

func TestEconomicJobQueueSQLiteBacklogAgeAndCounts(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	oldest := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "backlog-a")
	oldest.CreatedAt = now.Add(-2 * time.Hour)
	failed := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 2, "backlog-b")
	failed.CreatedAt = now.Add(-time.Hour)
	completed := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 3, "backlog-c")
	completed.CreatedAt = now.Add(-30 * time.Minute)
	for _, work := range []billing.EconomicRevisionWork{oldest, failed, completed} {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	}

	// Leave the oldest pending (claimed once, retried immediately), fail one
	// and complete one.
	oldestClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, oldestClaims, 1)
	require.NoError(t, store.RetryEconomicRevisionWorkWithReason(ctx, oldestClaims[0].Work, oldestClaims[0].Claim, billing.EconomicWorkReasonTransientFailure, time.Time{}))
	failedClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, failedClaims, 1)
	require.NoError(t, store.FailEconomicRevisionWork(ctx, failedClaims[0].Work, failedClaims[0].Claim, billing.EconomicWorkReasonPermanentFailure))
	completedClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, completedClaims, 1)
	require.NoError(t, store.CompleteEconomicRevisionWork(ctx, completedClaims[0].Work, completedClaims[0].Claim))

	backlog, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, backlog.Pending)
	require.Zero(t, backlog.Processing)
	require.Equal(t, 1, backlog.Completed)
	require.Equal(t, 1, backlog.Failed)
	require.Equal(t, oldest.CreatedAt.UnixNano(), backlog.OldestPendingAt.UnixNano())
	require.GreaterOrEqual(t, backlog.OldestPendingAge, 2*time.Hour)
	require.Zero(t, backlog.IncompleteDependencies)
}

func TestEconomicJobQueueSQLiteBacklogNextAttempt(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "next-attempt")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	claims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-a", time.Minute, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	due := time.Now().UTC().Add(45 * time.Minute)
	require.NoError(t, store.RetryEconomicRevisionWorkWithReason(ctx, claims[0].Work, claims[0].Claim, billing.EconomicWorkReasonRaterFailure, due))

	backlog, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, backlog.Pending)
	require.Equal(t, due.UnixNano(), backlog.NextAttemptAt.UnixNano())
}

func TestEconomicJobQueueSQLiteIncompleteDependenciesBlockClaimUntilRated(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "dependency-rating")
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
	require.Equal(t, 2, backlog.Pending+backlog.Processing)

	// Rating the dependency output satisfies the reconciliation work without
	// rewriting any queue state.
	require.NoError(t, store.AppendEconomicRevisionResult(ctx, rating, billing.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, rating)}))
	checks, err = store.EconomicRevisionDependencyChecks(ctx, reconciliation)
	require.NoError(t, err)
	require.Equal(t, billing.EconomicJobDependencySatisfied, checks[0].Status)
	backlog, err = store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Zero(t, backlog.IncompleteDependencies)

	reconciliationClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker-b", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, reconciliationClaims, 1)
	require.Equal(t, billing.EconomicWorkKindReconciliation, reconciliationClaims[0].Work.Kind)
	require.NoError(t, store.CompleteEconomicRevisionWork(ctx, reconciliationClaims[0].Work, reconciliationClaims[0].Claim))
}

func TestEconomicJobQueueSQLiteNeverMutatesAccountsOrHeads(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	tables := []string{
		"billing_accounts", "journal_transactions", "journal_entries", "call_exposures",
		"billing_valuations", "billing_reconciliations", "billing_economic_valuation_heads", "billing_unit_balances",
	}
	counts := func() map[string]int {
		out := make(map[string]int, len(tables))
		for _, table := range tables {
			var count int
			require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM `+table).Scan(ctx, &count))
			out[table] = count
		}
		return out
	}
	before := counts()

	rating := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "mutation-rating")
	reconciliation := economicJobSQLiteReconciliation(t, economicJobSQLiteDependency(t, rating))
	customer := economicJobSQLiteWork(t, billing.EconomicQueueCustomer, 1, "mutation-customer")
	for _, work := range []billing.EconomicRevisionWork{rating, reconciliation, customer} {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	}
	providerClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueProvider, "worker", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, providerClaims, 1)
	customerClaims, err := store.ClaimEconomicRevisionWorkBatch(ctx, billing.EconomicQueueCustomer, "worker", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, customerClaims, 1)
	_, err = store.HeartbeatEconomicRevisionWork(ctx, providerClaims[0].Work, providerClaims[0].Claim, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.RetryEconomicRevisionWorkWithReason(ctx, providerClaims[0].Work, providerClaims[0].Claim, billing.EconomicWorkReasonTransientFailure, time.Now().UTC()))
	require.NoError(t, store.FailEconomicRevisionWork(ctx, customerClaims[0].Work, customerClaims[0].Claim, billing.EconomicWorkReasonPermanentFailure))
	_, err = store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	_, err = store.EconomicRevisionDependencyChecks(ctx, reconciliation)
	require.NoError(t, err)

	require.Equal(t, before, counts())
}

func TestEconomicJobQueueSQLiteSchemaIsRegistered(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	require.Contains(t, RequiredMigrationNames, BillingEconomicJobQueueMigrationName)
	require.NoError(t, VerifySchema(ctx, store.db))
	for _, column := range []string{"work_kind", "dependency_count", "retry_reason", "failed_at_unix"} {
		var count int
		require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM pragma_table_info('billing_economic_revision_work_state') WHERE name = ?`, column).Scan(ctx, &count))
		require.Equal(t, 1, count, column)
	}
	var ddl string
	require.NoError(t, store.db.NewRaw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'billing_economic_revision_work_state'`).Scan(ctx, &ddl))
	require.Contains(t, ddl, "'failed'")
	var migrationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM bun_billing_migrations WHERE name = ?`, BillingEconomicJobQueueMigrationName).Scan(ctx, &migrationCount))
	require.Equal(t, 1, migrationCount)
}
