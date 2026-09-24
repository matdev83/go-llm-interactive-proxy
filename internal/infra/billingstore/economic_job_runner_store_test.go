package billingstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	reconciliationDependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	work.Dependencies = []billing.EconomicJobDependency{reconciliationDependency}
	return work
}

func economicJobRunnerStoreDependency(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
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

// seventhPassAllocationRater is the injected allocation-aware PostUsageRater
// used by the Phase 16 seventh-pass composition proofs. It returns the exact
// observation plane of the claimed work plus a trusted allocation coverage
// reference. The tamper switches deliberately drift the observation set or
// supply a stale full identity hash so the dual fence can be exercised through
// the real worker and job-runner composition.
type seventhPassAllocationRater struct {
	mu               sync.Mutex
	calls            int
	allocations      []economics.AllocationRef
	staleHash        string
	driftObservation bool
}

func (r *seventhPassAllocationRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	refs := append([]metering.ObservationRef(nil), input.ObservationRefs...)
	if len(refs) == 0 {
		for _, observation := range input.Observations {
			ref, err := observation.Ref(input.Subject.StoreID)
			if err != nil {
				return economics.Valuation{}, err
			}
			refs = append(refs, ref)
		}
	}
	if r.driftObservation && len(refs) > 0 {
		refs[0].ObservationID += "-seventhpass-drift"
	}
	valuation := economics.Valuation{
		ID: "seventhpass-allocation-rater-id", Version: economics.ValuationVersionV2,
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), r.allocations...),
		Completeness:           economics.CompletenessPartial,
		CreatedAt:              time.Unix(1_700_300_000, 0).UTC(),
	}
	if r.staleHash != "" {
		valuation.InputSetHash = r.staleHash
	}
	return valuation, nil
}

func seventhPassAllocationRef(storeID, id string, version uint64, fill string) economics.AllocationRef {
	return economics.AllocationRef{
		StoreID: storeID, AllocationID: id, Version: version, PayloadHash: strings.Repeat(fill, 64),
	}
}

// TestPhase16SeventhPassWorkerPersistsAllocationAwareRevision is the composed
// worker RED/GREEN proof: the actual EconomicRevisionWorker, an injected
// allocation-aware rater and the durable result writer must accept a valid
// allocation-inclusive revision, persist it, and advance the rebuildable head
// without ErrIdentityConflict while keeping the observation-only work fence.
func TestPhase16SeventhPassWorkerPersistsAllocationAwareRevision(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 41, "seventhpass-worker")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)

	alloc := seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-worker", 1, "c")
	rater := &seventhPassAllocationRater{allocations: []economics.AllocationRef{alloc}}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 4)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx), "allocation-aware revision must persist through the worker")
	require.Equal(t, 1, rater.calls)

	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{alloc}, valuation.AllocationCoverageRefs)

	// Dual identity: the stored valuation authenticates observation refs plus
	// allocation coverage, while the observation-only projection still equals
	// the immutable work envelope hash.
	observationHash, err := economics.CanonicalInputSetHash(normalized.Input.Basis, valuation.InputObservations)
	require.NoError(t, err)
	require.Equal(t, normalized.InputSetHash, observationHash)
	fullHash, err := economics.CanonicalValuationInputSetHash(normalized.Input.Basis, valuation.InputObservations, valuation.AllocationCoverageRefs)
	require.NoError(t, err)
	require.Equal(t, fullHash, valuation.InputSetHash)
	require.NotEqual(t, normalized.InputSetHash, valuation.InputSetHash)

	frozen, err := store.detailSelectedValuations(ctx, normalized.Subject.TenantID, []billing.SelectedCostValuationRef{{
		ValuationID: identity.ValuationKey(), Revision: 1, InputSetHash: valuation.InputSetHash,
	}})
	require.NoError(t, err)
	require.Len(t, frozen, 1, "the frozen allocation-aware selected identity must reload")
	require.Equal(t, []economics.AllocationRef{alloc}, frozen[0].AllocationCoverageRefs)

	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, normalized.InputSetHash, head.InputSetHash)
	require.Equal(t, identity.Key(), head.WorkID)
	require.Equal(t, identity.ValuationKey(), head.ValuationID)

	var status string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, store.StoreID(), identity.Key()).Scan(ctx, &status))
	require.Equal(t, "completed", status)
}

// TestPhase16SeventhPassJobRunnerPersistsAllocationAwareRevision is the
// composed EconomicJobRunner proof: the application runner over the same
// injected allocation-aware rater and durable writer must accept and persist
// the allocation-inclusive revision in one bounded run.
func TestPhase16SeventhPassJobRunnerPersistsAllocationAwareRevision(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 42, "seventhpass-runner")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)

	alloc := seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-runner", 1, "d")
	rater := &seventhPassAllocationRater{allocations: []economics.AllocationRef{alloc}}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, &economicJobRunnerStoreReconciler{}))
	require.NoError(t, err)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Completed)
	require.Zero(t, summary.Failed)
	require.Zero(t, summary.Retried)
	require.Equal(t, 1, rater.calls)

	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{alloc}, valuation.AllocationCoverageRefs)
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, normalized.InputSetHash, head.InputSetHash)
	require.Equal(t, identity.ValuationKey(), head.ValuationID)
}

// TestPhase16SeventhPassWorkerRejectsTamperedAllocationRevisions proves the
// observation fence survives the allocation-aware acceptance path: a rater that
// drifts the observation plane or supplies a stale full allocation-aware hash
// is rejected before persistence.
func TestPhase16SeventhPassWorkerRejectsTamperedAllocationRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("drifted observation set", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		work := economicJobRunnerStoreWork(t, billing.EconomicQueueCustomer, 43, "seventhpass-drift")
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
		identity, err := work.Identity()
		require.NoError(t, err)
		rater := &seventhPassAllocationRater{
			allocations:      []economics.AllocationRef{seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-drift", 1, "e")},
			driftObservation: true,
		}
		worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueCustomer, 4)
		require.NoError(t, err)
		err = worker.ProcessOnce(ctx)
		require.ErrorIs(t, err, billing.ErrEconomicRevisionInputMismatch)
		_, getErr := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
		require.Error(t, getErr, "a drifted observation set must not persist a valuation")
	})

	t.Run("stale allocation-aware hash", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		work := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 43, "seventhpass-stale")
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
		identity, err := work.Identity()
		require.NoError(t, err)
		rater := &seventhPassAllocationRater{
			allocations: []economics.AllocationRef{seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-stale", 1, "f")},
			staleHash:   strings.Repeat("9", 64),
		}
		worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 4)
		require.NoError(t, err)
		err = worker.ProcessOnce(ctx)
		require.ErrorIs(t, err, billing.ErrEconomicRevisionInputMismatch)
		_, getErr := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
		require.Error(t, getErr, "a stale full identity hash must not persist a valuation")
	})
}
