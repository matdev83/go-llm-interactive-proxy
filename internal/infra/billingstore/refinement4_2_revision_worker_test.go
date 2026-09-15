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

func refinement42Work(t *testing.T, queue billing.EconomicQueue, revision uint64, quantity string) billing.EconomicRevisionWork {
	t.Helper()
	observation := phase4EconomicsObservation("test", "refinement42-"+queue.String()+"-"+quantity, revision)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	return billing.EconomicRevisionWork{
		Queue: queue, HeadKey: "refinement42-head", Subject: observation.Subject,
		EvidenceRevision: revision, Input: input,
		CreatedAt: time.Unix(1_700_010_000+int64(revision), 0).UTC(),
	}
}

type refinement42Rater struct {
	mu    sync.Mutex
	calls int
}

func (r *refinement42Rater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	return economics.Valuation{
		ID: "unstable-rater-id", Version: economics.ValuationVersionV2,
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(1_700_020_000+int64(call), 0).UTC(),
	}, nil
}

type refinement42Reconciler struct{}

func (refinement42Reconciler) Reconcile(_ context.Context, work billing.EconomicRevisionWork, _ economics.Valuation) (*billing.EconomicReconciliation, error) {
	return &billing.EconomicReconciliation{
		ID: "unstable-reconciliation-id", Version: work.EvidenceRevision,
		Subject: work.Subject, Scope: work.Input.Scope, Basis: work.Input.Basis,
		InputSetHash: work.InputSetHash, ResultJSON: []byte(`{"status":"partial"}`),
		CreatedAt: time.Unix(1_700_020_100, 0).UTC(),
	}, nil
}

type refinement42TimestampReconciler struct {
	mu    sync.Mutex
	calls int
}

func (r *refinement42TimestampReconciler) Reconcile(_ context.Context, work billing.EconomicRevisionWork, _ economics.Valuation) (*billing.EconomicReconciliation, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	return &billing.EconomicReconciliation{
		ID: "unstable-reconciliation-id", Version: work.EvidenceRevision,
		Subject: work.Subject, Scope: work.Input.Scope, Basis: work.Input.Basis,
		InputSetHash: work.InputSetHash, ResultJSON: []byte(`{"status":"partial"}`),
		CreatedAt: time.Unix(1_700_020_100+int64(call), 0).UTC(),
	}, nil
}

// refinement42ResultSink intentionally hides DurableStore's result probe so
// duplicate/retry tests execute the rater and reconciler again. It can report
// an ambiguous error after the first durable commit to exercise convergence.
type refinement42ResultSink struct {
	store          *DurableStore
	mu             sync.Mutex
	calls          int
	failAfterFirst bool
	payloads       [][]byte
}

func (s *refinement42ResultSink) AppendEconomicRevisionResult(ctx context.Context, work billing.EconomicRevisionWork, result billing.EconomicRevisionResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.payloads = append(s.payloads, append([]byte(nil), payload...))
	s.mu.Unlock()
	if err := s.store.AppendEconomicRevisionResult(ctx, work, result); err != nil {
		return err
	}
	if s.failAfterFirst && call == 1 {
		return errors.New("ambiguous economic revision result commit")
	}
	return nil
}

// refinement42ReplayWorkReader leaves the durable marker pending so two
// independently constructed workers can both process the same immutable work.
type refinement42ReplayWorkReader struct{ store *DurableStore }

func (r refinement42ReplayWorkReader) ListPendingEconomicRevisionWork(ctx context.Context, queue billing.EconomicQueue, limit int) ([]billing.EconomicRevisionWork, error) {
	return r.store.ListPendingEconomicRevisionWork(ctx, queue, limit)
}

type refinement42FailOnceRater struct {
	mu     sync.Mutex
	calls  int
	failed bool
}

func (r *refinement42FailOnceRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	r.calls++
	shouldFail := !r.failed
	if shouldFail {
		r.failed = true
	}
	r.mu.Unlock()
	if shouldFail {
		return economics.Valuation{}, errors.New("injected economic revision valuation failure")
	}
	refs := make([]metering.ObservationRef, 0, len(input.Observations))
	for _, observation := range input.Observations {
		ref, err := observation.Ref(input.Subject.StoreID)
		if err != nil {
			return economics.Valuation{}, err
		}
		refs = append(refs, ref)
	}
	return economics.Valuation{
		ID: "unstable-rater-id", Version: economics.ValuationVersionV2,
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		Completeness: economics.CompletenessPartial, CreatedAt: time.Unix(1_700_020_000, 0).UTC(),
	}, nil
}

func TestRefinement42DurableRevisionWorkReplayAndQueueIsolation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	customer := refinement42Work(t, billing.EconomicQueueCustomer, 1, "3")
	provider := refinement42Work(t, billing.EconomicQueueProvider, 1, "4")
	correction := refinement42Work(t, billing.EconomicQueueCustomer, 2, "8")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, customer))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, customer))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, provider))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, correction))
	customerItems, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueCustomer, 16)
	require.NoError(t, err)
	require.Len(t, customerItems, 2)
	providerItems, err := store.ListPendingEconomicRevisionWork(ctx, billing.EconomicQueueProvider, 16)
	require.NoError(t, err)
	require.Len(t, providerItems, 1)
	firstID, err := customerItems[0].Identity()
	require.NoError(t, err)
	secondID, err := customerItems[1].Identity()
	require.NoError(t, err)
	require.NotEqual(t, firstID.Key(), secondID.Key())
}

func TestRefinement42SameRevisionInputHashIgnoresTransportMetadata(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	first := refinement42Work(t, billing.EconomicQueueCustomer, 1, "3")
	replay := first
	replay.CreatedAt = first.CreatedAt.Add(15 * time.Minute)
	replay.Input = first.Input.Clone()
	replay.Input.Observations[0].ReceivedAt = first.Input.Observations[0].ReceivedAt.Add(30 * time.Minute)

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	// Source revision and input hash are the economic identity. Receipt time is
	// transport metadata and must not turn a terminal replay into a conflict.
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, replay))
	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_economic_work`).Scan(ctx, &count))
	require.Equal(t, 1, count)
}

func TestRefinement42RevisionWorkerPersistsPureHeadWithoutBalanceMutation(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	first := refinement42Work(t, billing.EconomicQueueCustomer, 1, "3")
	second := refinement42Work(t, billing.EconomicQueueCustomer, 2, "8")
	// Deliberately enqueue newest first to exercise deterministic head ordering.
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	rater := &refinement42Rater{}
	worker, err := billing.NewEconomicRevisionWorkerWithReconciler(store, store, rater, refinement42Reconciler{}, billing.EconomicQueueCustomer, 16)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	secondID, err := second.Identity()
	require.NoError(t, err)
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueCustomer, second.HeadKey)
	require.NoError(t, err)
	require.Equal(t, secondID.Key(), head.WorkID)
	require.Equal(t, second.EvidenceRevision, head.EvidenceRevision)
	require.Equal(t, secondID.ValuationKey(), head.ValuationID)
	require.NotEmpty(t, head.ReconciliationID)

	var valuationCount, reconciliationCount, journalCount, accountBalanceCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_valuations`).Scan(ctx, &valuationCount))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_reconciliations`).Scan(ctx, &reconciliationCount))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM journal_transactions`).Scan(ctx, &journalCount))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_unit_balances`).Scan(ctx, &accountBalanceCount))
	require.Equal(t, 2, valuationCount)
	require.Equal(t, 2, reconciliationCount)
	require.Zero(t, journalCount)
	require.Zero(t, accountBalanceCount)
}

func TestRefinement42RevisionWorkerRestartReplayDoesNotRerate(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := refinement42Work(t, billing.EconomicQueueProvider, 3, "9")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	firstRater := &refinement42Rater{}
	first, err := billing.NewEconomicRevisionWorker(store, store, firstRater, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, first.ProcessOnce(ctx))
	require.Equal(t, 1, firstRater.calls)
	secondRater := &refinement42Rater{}
	restarted, err := billing.NewEconomicRevisionWorker(store, store, secondRater, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessOnce(ctx))
	require.Zero(t, secondRater.calls)
	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_valuations`).Scan(ctx, &valuationCount))
	require.Equal(t, 1, valuationCount)
}

func TestRefinement42RevisionQueueProgressRetiresCompletedPageAndProcessesLateCorrection(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	for revision := uint64(1); revision <= 3; revision++ {
		require.NoError(t, store.AppendEconomicRevisionWork(ctx, refinement42Work(t, billing.EconomicQueueCustomer, revision, string(rune('0'+revision)))))
	}
	rater := &refinement42Rater{}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueCustomer, 2)
	require.NoError(t, err)

	// A bounded page retires its first two records while the third remains due.
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	var completed int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND status = 'completed'`, "test").Scan(ctx, &completed))
	require.Equal(t, 2, completed)

	// A newer late correction must not be hidden behind the completed first page.
	correction := refinement42Work(t, billing.EconomicQueueCustomer, 4, "late-correction")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, correction))
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 4, rater.calls)
	var pending int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND status <> 'completed'`, "test").Scan(ctx, &pending))
	require.Zero(t, pending)

	identity, err := correction.Identity()
	require.NoError(t, err)
	head, err := store.GetEconomicValuationHead(ctx, correction.Queue, correction.HeadKey)
	require.NoError(t, err)
	require.Equal(t, identity.Key(), head.WorkID)
	var immutableWork, valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ?`, "test").Scan(ctx, &immutableWork))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, "test").Scan(ctx, &valuationCount))
	require.Equal(t, 4, immutableWork)
	require.Equal(t, 4, valuationCount)

	// Restart sees only durably completed state and does not re-rate history.
	restartedRater := &refinement42Rater{}
	restarted, err := billing.NewEconomicRevisionWorker(store, store, restartedRater, billing.EconomicQueueCustomer, 2)
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessOnce(ctx))
	require.Zero(t, restartedRater.calls)
}

func TestRefinement42RevisionQueueFailureRemainsAvailableUntilSuccessfulRetirement(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := refinement42Work(t, billing.EconomicQueueProvider, 7, "retry-me")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	rater := &refinement42FailOnceRater{}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)

	require.Error(t, worker.ProcessOnce(ctx))
	var status, lastError, leaseOwner string
	var attempts int
	var leaseUntil int64
	require.NoError(t, store.db.NewRaw(`SELECT status, attempt_count, lease_owner, lease_until_unix, last_error FROM billing_economic_revision_work_state WHERE store_id = ?`, "test").Scan(ctx, &status, &attempts, &leaseOwner, &leaseUntil, &lastError))
	require.Equal(t, "pending", status)
	require.Equal(t, 1, attempts)
	require.Empty(t, leaseOwner)
	require.Zero(t, leaseUntil)
	require.Contains(t, lastError, "injected economic revision valuation failure")
	_, err = store.GetEconomicValuationHead(ctx, work.Queue, work.HeadKey)
	require.ErrorIs(t, err, ErrEconomicRevisionHeadNotFound)

	// Failed work remains due and is retried; successful output/head are kept
	// immutable while the queue state is retired separately.
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	require.NoError(t, store.db.NewRaw(`SELECT status, attempt_count, lease_owner, lease_until_unix FROM billing_economic_revision_work_state WHERE store_id = ?`, "test").Scan(ctx, &status, &attempts, &leaseOwner, &leaseUntil))
	require.Equal(t, "completed", status)
	require.Equal(t, 2, attempts)
	require.Empty(t, leaseOwner)
	require.Zero(t, leaseUntil)
	identity, err := work.Identity()
	require.NoError(t, err)
	head, err := store.GetEconomicValuationHead(ctx, work.Queue, work.HeadKey)
	require.NoError(t, err)
	require.Equal(t, identity.Key(), head.WorkID)
	var immutableWork, valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_work WHERE store_id = ?`, "test").Scan(ctx, &immutableWork))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, "test").Scan(ctx, &valuationCount))
	require.Equal(t, 1, immutableWork)
	require.Equal(t, 1, valuationCount)
}

func TestRefinement42RevisionQueueRetryDoesNotStarveNewWork(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	failed := refinement42Work(t, billing.EconomicQueueProvider, 12, "failed-first")
	later := refinement42Work(t, billing.EconomicQueueProvider, 13, "later-work")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, failed))
	rater := &refinement42FailOnceRater{}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	require.Error(t, worker.ProcessOnce(ctx))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, later))

	// The never-attempted later item is selected before the failed retry, so a
	// bounded page cannot be monopolized by a permanently failing head row.
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	var completed, pending int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND status = 'completed'`, "test").Scan(ctx, &completed))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND status <> 'completed'`, "test").Scan(ctx, &pending))
	require.Equal(t, 1, completed)
	require.Equal(t, 1, pending)

	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 3, rater.calls)
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND status = 'completed'`, "test").Scan(ctx, &completed))
	require.Equal(t, 2, completed)
	require.Zero(t, pendingStateCount(t, store, ctx))
	identity, err := later.Identity()
	require.NoError(t, err)
	head, err := store.GetEconomicValuationHead(ctx, later.Queue, later.HeadKey)
	require.NoError(t, err)
	require.Equal(t, identity.Key(), head.WorkID)
}

func pendingStateCount(t *testing.T, store *DurableStore, ctx context.Context) int {
	t.Helper()
	var pending int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_economic_revision_work_state WHERE store_id = ? AND status <> 'completed'`, "test").Scan(ctx, &pending))
	return pending
}

func TestRefinement42RevisionQueueExpiredClaimIsFenced(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := refinement42Work(t, billing.EconomicQueueCustomer, 11, "fenced")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))

	first, claimed, err := store.ClaimEconomicRevisionWork(ctx, work, "worker-a", time.Hour)
	require.NoError(t, err)
	require.True(t, claimed)
	_, claimed, err = store.ClaimEconomicRevisionWork(ctx, work, "worker-b", time.Hour)
	require.NoError(t, err)
	require.False(t, claimed, "a live claim must exclude another worker")

	// Simulate lease expiry without sleeping; the next worker must advance the
	// fence before it can retire the same immutable marker.
	identity, err := work.Identity()
	require.NoError(t, err)
	_, err = store.db.NewRaw(`UPDATE billing_economic_revision_work_state SET lease_until_unix = 0 WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Exec(ctx)
	require.NoError(t, err)
	second, claimed, err := store.ClaimEconomicRevisionWork(ctx, work, "worker-b", time.Hour)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Greater(t, second.Fence, first.Fence)
	require.ErrorIs(t, store.CompleteEconomicRevisionWork(ctx, work, first), billing.ErrEconomicRevisionClaimLost)
	require.NoError(t, store.CompleteEconomicRevisionWork(ctx, work, second))
	var status string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status))
	require.Equal(t, "completed", status)
}

func TestRefinement42RevisionRetryConvergesAcrossDerivedTimestamps(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := refinement42Work(t, billing.EconomicQueueCustomer, 21, "13")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	rater := &refinement42Rater{}
	reconciler := &refinement42TimestampReconciler{}
	sink := &refinement42ResultSink{store: store, failAfterFirst: true}
	worker, err := billing.NewEconomicRevisionWorkerWithReconciler(store, sink, rater, reconciler, billing.EconomicQueueCustomer, 1)
	require.NoError(t, err)

	// The first result commits before the sink reports an ambiguous failure. A
	// retry must replay the same immutable result even though both calculators
	// supply a fresh nonzero wall-clock timestamp.
	require.Error(t, worker.ProcessOnce(ctx))
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	require.Equal(t, 2, reconciler.calls)
	require.Len(t, sink.payloads, 2)
	require.Equal(t, string(sink.payloads[0]), string(sink.payloads[1]))

	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, work.CreatedAt, valuation.CreatedAt)
	reconciliation, err := store.GetReconciliation(ctx, identity.ReconciliationKey(), identity.EvidenceRevision)
	require.NoError(t, err)
	require.Equal(t, work.CreatedAt, reconciliation.CreatedAt)
	var status string
	require.NoError(t, store.db.NewRaw(`SELECT status FROM billing_economic_revision_work_state WHERE store_id = ? AND work_id = ?`, "test", identity.Key()).Scan(ctx, &status))
	require.Equal(t, "completed", status)
}

func TestRefinement42DuplicateWorkersConvergeAcrossDerivedTimestamps(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := refinement42Work(t, billing.EconomicQueueProvider, 22, "17")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	reader := refinement42ReplayWorkReader{store: store}
	sink := &refinement42ResultSink{store: store}
	rater := &refinement42Rater{}
	reconciler := &refinement42TimestampReconciler{}
	first, err := billing.NewEconomicRevisionWorkerWithReconciler(reader, sink, rater, reconciler, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)
	second, err := billing.NewEconomicRevisionWorkerWithReconciler(reader, sink, rater, reconciler, billing.EconomicQueueProvider, 1)
	require.NoError(t, err)

	// The replay reader keeps the marker pending so two workers independently
	// process the same identity. Durable valuation/reconciliation rows must
	// accept the second result as an identical replay.
	require.NoError(t, first.ProcessOnce(ctx))
	require.NoError(t, second.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	require.Equal(t, 2, reconciler.calls)
	require.Len(t, sink.payloads, 2)
	require.Equal(t, string(sink.payloads[0]), string(sink.payloads[1]))

	identity, err := work.Identity()
	require.NoError(t, err)
	valuation, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, work.CreatedAt, valuation.CreatedAt)
	reconciliation, err := store.GetReconciliation(ctx, identity.ReconciliationKey(), identity.EvidenceRevision)
	require.NoError(t, err)
	require.Equal(t, work.CreatedAt, reconciliation.CreatedAt)
	var valuationCount, reconciliationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_valuations WHERE store_id = ? AND valuation_id = ?`, "test", identity.ValuationKey()).Scan(ctx, &valuationCount))
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(*) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ?`, "test", identity.ReconciliationKey()).Scan(ctx, &reconciliationCount))
	require.Equal(t, 1, valuationCount)
	require.Equal(t, 1, reconciliationCount)
}
