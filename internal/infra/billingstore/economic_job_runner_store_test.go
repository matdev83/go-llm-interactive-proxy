package billingstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func economicJobRunnerStoreWork(t *testing.T, queue billing.EconomicQueue, revision uint64, label string) billing.EconomicRevisionWork {
	t.Helper()
	observation := phase4EconomicsObservation("test", "job-runner-"+label, revision)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	return billing.EconomicRevisionWork{
		Queue: queue, HeadKey: "economic-job-runner-head-" + label,
		Subject: observation.Subject, EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_100_000+int64(revision), 0).UTC(),
	}
}

func economicJobRunnerStoreReconciliation(t *testing.T, dependency billing.EconomicRevisionWork) billing.EconomicRevisionWork {
	t.Helper()
	work := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 7, "reconciliation")
	work.Kind = billing.EconomicWorkKindReconciliation
	normalized, err := dependency.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	work.Dependencies = []billing.EconomicJobDependency{{
		Kind: billing.EconomicWorkKindForQueue(normalized.Queue), Queue: normalized.Queue,
		HeadKey: normalized.HeadKey, EvidenceRevision: identity.EvidenceRevision, InputSetHash: identity.InputSetHash,
	}}
	return work
}

func economicJobRunnerStoreDependency(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicJobDependency {
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

type economicJobRunnerStoreReconciler struct {
	mu    sync.Mutex
	calls []billing.EconomicJobDependencyOutput
}

func (r *economicJobRunnerStoreReconciler) ReconcileJob(_ context.Context, work billing.EconomicRevisionWork, outputs []billing.EconomicJobDependencyOutput) (*billing.EconomicReconciliation, error) {
	r.mu.Lock()
	r.calls = append(r.calls, outputs...)
	r.mu.Unlock()
	return &billing.EconomicReconciliation{
		Subject: work.Subject, Scope: work.Input.Scope, Basis: work.Input.Basis,
		InputSetHash: work.InputSetHash, ResultJSON: json.RawMessage(`{"status":"matched"}`),
		CreatedAt: time.Unix(1_700_100_500, 0).UTC(),
	}, nil
}

func economicJobRunnerStoreConfig(store *DurableStore, rater billing.PostUsageRater, reconciler billing.EconomicJobReconciler) billing.EconomicJobRunnerConfig {
	return billing.EconomicJobRunnerConfig{
		Queue: store, Backlog: store, Results: store, Dependencies: store, Reconciliations: store,
		Rater: rater, Reconciler: reconciler, Owner: "runner-store-test", Batch: 8,
	}
}

// economicJobRunnerCompleteFailStore injects one completion failure while
// leaving every other durable queue operation intact.
type economicJobRunnerCompleteFailStore struct {
	*DurableStore
	mu       sync.Mutex
	failures int
}

func (s *economicJobRunnerCompleteFailStore) CompleteEconomicRevisionWork(ctx context.Context, work billing.EconomicRevisionWork, claim billing.EconomicRevisionWorkClaim) error {
	s.mu.Lock()
	if s.failures > 0 {
		s.failures--
		s.mu.Unlock()
		return errors.New("injected economic job runner completion failure")
	}
	s.mu.Unlock()
	return s.DurableStore.CompleteEconomicRevisionWork(ctx, work, claim)
}

func TestEconomicJobRunnerSQLiteProviderRatingPromotesReconciliation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	rating := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 1, "rating")
	reconciliation := economicJobRunnerStoreReconciliation(t, rating)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, rating))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))
	rater := &refinement42Rater{}
	reconciler := &economicJobRunnerStoreReconciler{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
	require.NoError(t, err)

	before, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, before.Pending)
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
	require.Equal(t, 1, rater.calls, "reconciliation work must not be rated")
	require.Len(t, reconciler.calls, 1)
	dependency := economicJobRunnerStoreDependency(t, rating)
	dependencyIdentity, err := dependency.OutputIdentity()
	require.NoError(t, err)
	require.Equal(t, dependencyIdentity.ValuationKey(), reconciler.calls[0].Valuation.ID)

	identity, err := reconciliation.Identity()
	require.NoError(t, err)
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ?`,
		"test", identity.ReconciliationKey(), int64(identity.EvidenceRevision)).Scan(ctx, &count))
	require.Equal(t, 1, count)
	probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
	require.NoError(t, err)
	require.True(t, probed)
}

func TestEconomicJobRunnerSQLiteInterruptionAfterOutputReplaysOnce(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobRunnerStoreWork(t, billing.EconomicQueueCustomer, 1, "interrupted")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)
	failing := &economicJobRunnerCompleteFailStore{DurableStore: store, failures: 1}
	rater := &refinement42Rater{}
	failingConfig := economicJobRunnerStoreConfig(store, rater, &economicJobRunnerStoreReconciler{})
	failingConfig.Queue = failing
	first, err := billing.NewEconomicJobRunner(failingConfig)
	require.NoError(t, err)
	firstSummary, err := first.RunOnce(ctx, billing.EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, firstSummary.Retried)
	require.Zero(t, firstSummary.Completed)
	require.Equal(t, 1, rater.calls)
	var status string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status))
	require.Equal(t, "pending", status)

	restarted, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, &economicJobRunnerStoreReconciler{}))
	require.NoError(t, err)
	secondSummary, err := restarted.RunOnce(ctx, billing.EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, secondSummary.Completed)
	require.Equal(t, 1, rater.calls, "a durable output must not be recomputed after interruption")
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status))
	require.Equal(t, "completed", status)
	var valuations int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, "test").Scan(ctx, &valuations))
	require.Equal(t, 1, valuations)
}

func TestEconomicJobRunnerSQLiteNeverMutatesFinancialTables(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	financialTables := []string{
		"billing_accounts", "journal_transactions", "journal_entries", "call_exposures",
		"billing_provider_cost_heads", "billing_selected_cost_adjustments", "billing_unit_balances",
	}
	counts := func() map[string]int {
		out := make(map[string]int, len(financialTables))
		for _, table := range financialTables {
			var count int
			require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM `+table).Scan(ctx, &count))
			out[table] = count
		}
		return out
	}
	before := counts()

	rating := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 1, "financial-rating")
	reconciliation := economicJobRunnerStoreReconciliation(t, rating)
	customer := economicJobRunnerStoreWork(t, billing.EconomicQueueCustomer, 1, "financial-customer")
	for _, work := range []billing.EconomicRevisionWork{rating, reconciliation, customer} {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, &refinement42Rater{}, &economicJobRunnerStoreReconciler{}))
	require.NoError(t, err)
	providerSummary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, providerSummary.Completed)
	customerSummary, err := runner.RunOnce(ctx, billing.EconomicQueueCustomer)
	require.NoError(t, err)
	require.Equal(t, 1, customerSummary.Completed)
	promoted, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, promoted.Completed)

	require.Equal(t, before, counts())
	var valuations, reconciliations int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, "test").Scan(ctx, &valuations))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ?`, "test").Scan(ctx, &reconciliations))
	require.Equal(t, 2, valuations)
	require.Equal(t, 1, reconciliations)
}
