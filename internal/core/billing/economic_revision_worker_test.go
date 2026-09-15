package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// economicRevisionTestQueue is deliberately append-only. It models the
// durable journal-to-worker handoff and lets the worker prove that a replay is
// a no-op after the result identity has been durably recorded.
type economicRevisionTestQueue struct {
	mu    sync.Mutex
	items []EconomicRevisionWork
}

func (q *economicRevisionTestQueue) Append(_ context.Context, work EconomicRevisionWork) error {
	work, err := work.Normalize()
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	identity, err := work.Identity()
	if err != nil {
		return err
	}
	for _, existing := range q.items {
		existingIdentity, identityErr := existing.Identity()
		if identityErr == nil && existingIdentity.Key() == identity.Key() {
			return nil
		}
	}
	q.items = append(q.items, work)
	return nil
}

func (q *economicRevisionTestQueue) ListPendingEconomicRevisionWork(_ context.Context, queue EconomicQueue, limit int) ([]EconomicRevisionWork, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := make([]EconomicRevisionWork, 0, len(q.items))
	for _, item := range q.items {
		if item.Queue == queue {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].EvidenceRevision != items[j].EvidenceRevision {
			return items[i].EvidenceRevision < items[j].EvidenceRevision
		}
		return items[i].InputSetHash < items[j].InputSetHash
	})
	if limit < len(items) {
		items = items[:limit]
	}
	return items, nil
}

type economicRevisionTestResultStore struct {
	mu          sync.Mutex
	results     map[string]EconomicRevisionResult
	heads       map[string]EconomicValuationHead
	persisted   []EconomicRevisionIdentity
	balanceLock int
}

func newEconomicRevisionTestResultStore() *economicRevisionTestResultStore {
	return &economicRevisionTestResultStore{
		results: make(map[string]EconomicRevisionResult),
		heads:   make(map[string]EconomicValuationHead),
	}
}

func (s *economicRevisionTestResultStore) HasEconomicRevisionResult(_ context.Context, identity EconomicRevisionIdentity) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.results[identity.Key()]
	return ok, nil
}

func (s *economicRevisionTestResultStore) AppendEconomicRevisionResult(_ context.Context, work EconomicRevisionWork, result EconomicRevisionResult) error {
	work, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := work.Identity()
	if err != nil {
		return err
	}
	valuation := result.Valuation.Clone()
	if valuation.ID != identity.ValuationKey() {
		return errors.New("test result store requires revision-keyed valuation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.results[identity.Key()]; ok {
		if existing.Valuation.Fingerprint() != valuation.Fingerprint() {
			return errors.New("test result store identity conflict")
		}
		return nil
	}
	s.results[identity.Key()] = result
	s.persisted = append(s.persisted, identity)
	key := work.Queue.String() + "\x00" + work.HeadKey
	current, ok := s.heads[key]
	if !ok || current.IsOlderThan(identity) {
		s.heads[key] = EconomicValuationHead{
			Queue:            work.Queue,
			HeadKey:          work.HeadKey,
			Subject:          work.Subject,
			EvidenceRevision: work.EvidenceRevision,
			InputSetHash:     work.InputSetHash,
			WorkID:           identity.Key(),
			ValuationID:      valuation.ID,
			ValuationVersion: valuation.Version,
			Fingerprint:      valuation.Fingerprint(),
		}
	}
	return nil
}

type economicRevisionTestRater struct {
	mu    sync.Mutex
	calls []economics.PostUsageRatingInput
}

func (r *economicRevisionTestRater) Rate(_ context.Context, in economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	r.calls = append(r.calls, in.Clone())
	r.mu.Unlock()
	refs := append([]metering.ObservationRef(nil), in.ObservationRefs...)
	if len(refs) == 0 {
		for _, observation := range in.Observations {
			ref, err := observation.Ref(in.Subject.StoreID)
			if err != nil {
				return economics.Valuation{}, err
			}
			refs = append(refs, ref)
		}
	}
	return economics.Valuation{
		ID:                "rater-generated-id",
		Version:           economics.ValuationVersionV2,
		Perspective:       in.Perspective,
		Basis:             in.Basis,
		Subject:           in.Subject,
		Scope:             in.Scope,
		InputObservations: refs,
		Completeness:      economics.CompletenessPartial,
		CreatedAt:         time.Unix(1_700_000_000+int64(len(r.calls)), 0).UTC(),
	}, nil
}

type economicRevisionTestReconciler struct {
	mu    sync.Mutex
	calls []EconomicRevisionWork
}

func (r *economicRevisionTestReconciler) Reconcile(_ context.Context, work EconomicRevisionWork, _ economics.Valuation) (*EconomicReconciliation, error) {
	r.mu.Lock()
	r.calls = append(r.calls, work)
	r.mu.Unlock()
	return &EconomicReconciliation{
		ID:           "reconciliation-generated-id",
		Version:      work.EvidenceRevision,
		Subject:      work.Subject,
		Scope:        work.Input.Scope,
		Basis:        work.Input.Basis,
		InputSetHash: work.InputSetHash,
		ResultJSON:   json.RawMessage(`{"status":"partial"}`),
		CreatedAt:    time.Unix(1_700_000_500+int64(work.EvidenceRevision), 0).UTC(),
	}, nil
}

type economicRevisionTimestampRater struct {
	mu         sync.Mutex
	calls      int
	timestamps []time.Time
}

func (r *economicRevisionTimestampRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	r.calls++
	createdAt := time.Unix(1_700_040_000+int64(r.calls), 0).UTC()
	r.timestamps = append(r.timestamps, createdAt)
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
	return economics.Valuation{
		ID:                "timestamp-varying-rater-id",
		Version:           economics.ValuationVersionV2,
		Perspective:       input.Perspective,
		Basis:             input.Basis,
		Subject:           input.Subject,
		Scope:             input.Scope,
		InputObservations: refs,
		Completeness:      economics.CompletenessPartial,
		CreatedAt:         createdAt,
	}, nil
}

type economicRevisionTimestampReconciler struct {
	mu         sync.Mutex
	calls      int
	timestamps []time.Time
}

func (r *economicRevisionTimestampReconciler) Reconcile(_ context.Context, work EconomicRevisionWork, _ economics.Valuation) (*EconomicReconciliation, error) {
	r.mu.Lock()
	r.calls++
	createdAt := time.Unix(1_700_041_000+int64(r.calls), 0).UTC()
	r.timestamps = append(r.timestamps, createdAt)
	r.mu.Unlock()
	return &EconomicReconciliation{
		ID:           "timestamp-varying-reconciliation-id",
		Version:      work.EvidenceRevision,
		Subject:      work.Subject,
		Scope:        work.Input.Scope,
		Basis:        work.Input.Basis,
		InputSetHash: work.InputSetHash,
		ResultJSON:   json.RawMessage(`{"status":"partial"}`),
		CreatedAt:    createdAt,
	}, nil
}

// economicRevisionReplayResultStore models a durable result identity fence. It
// records a successful append before optionally returning an ambiguous error,
// so a retry must be byte-identical to converge rather than conflict.
type economicRevisionReplayResultStore struct {
	mu             sync.Mutex
	appendCalls    int
	failAfterFirst bool
	payloads       [][]byte
	results        []EconomicRevisionResult
}

func (s *economicRevisionReplayResultStore) AppendEconomicRevisionResult(_ context.Context, _ EconomicRevisionWork, result EconomicRevisionResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendCalls++
	s.payloads = append(s.payloads, append([]byte(nil), payload...))
	s.results = append(s.results, result)
	if len(s.payloads) > 1 && !bytes.Equal(s.payloads[0], payload) {
		return ErrEconomicRevisionConflict
	}
	if s.failAfterFirst && s.appendCalls == 1 {
		return errors.New("ambiguous economic revision result commit")
	}
	return nil
}

func economicRevisionTestWork(t *testing.T, queue EconomicQueue, revision uint64, quantity string) EconomicRevisionWork {
	t.Helper()
	observation := phase9Observation(t, "revision-"+queue.String()+"-"+quantity, metering.OriginProvider, metering.ComponentKey{
		Direction: metering.DirectionOutput,
		Component: metering.ComponentTextToken,
		Unit:      metering.UnitToken,
		SchemaID:  "revision-worker-v1",
	}, quantity)
	input := phase9RatingInput(t, economics.BasisProviderReported, []metering.Observation{observation})
	return EconomicRevisionWork{
		Queue:            queue,
		HeadKey:          "b-leg-revision-head",
		Subject:          input.Subject,
		EvidenceRevision: revision,
		Input:            input,
		CreatedAt:        time.Unix(1_700_001_000+int64(revision), 0).UTC(),
	}
}

func TestEconomicRevisionWorker_PreterminalRevisionValuationAndReconciliation(t *testing.T) {
	t.Parallel()
	queue := &economicRevisionTestQueue{}
	store := newEconomicRevisionTestResultStore()
	rater := &economicRevisionTestRater{}
	reconciler := &economicRevisionTestReconciler{}
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	require.NoError(t, queue.Append(context.Background(), work))
	worker, err := NewEconomicRevisionWorkerWithReconciler(queue, store, rater, reconciler, EconomicQueueCustomer, 8)
	require.NoError(t, err)

	require.NoError(t, worker.ProcessOnce(context.Background()))
	identity, err := work.Identity()
	require.NoError(t, err)
	require.Len(t, rater.calls, 1)
	require.Len(t, reconciler.calls, 1)
	require.Contains(t, store.results, identity.Key())
	require.Equal(t, identity.ValuationKey(), store.results[identity.Key()].Valuation.ID)
	require.NotNil(t, store.results[identity.Key()].Reconciliation)
	_, ok := store.heads[work.Queue.String()+"\x00"+work.HeadKey]
	require.True(t, ok)
}

func TestEconomicRevisionWorker_TerminalReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	queue := &economicRevisionTestQueue{}
	store := newEconomicRevisionTestResultStore()
	rater := &economicRevisionTestRater{}
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 2, "4")
	require.NoError(t, queue.Append(context.Background(), work))
	worker, err := NewEconomicRevisionWorker(queue, store, rater, EconomicQueueCustomer, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(context.Background()))
	require.NoError(t, worker.ProcessOnce(context.Background()))
	require.Len(t, rater.calls, 1)
	require.Len(t, store.persisted, 1)
}

func TestEconomicRevisionWorker_LateCorrectionUsesNewIdentityAndHead(t *testing.T) {
	t.Parallel()
	queue := &economicRevisionTestQueue{}
	store := newEconomicRevisionTestResultStore()
	rater := &economicRevisionTestRater{}
	first := economicRevisionTestWork(t, EconomicQueueProvider, 1, "5")
	correction := economicRevisionTestWork(t, EconomicQueueProvider, 2, "8")
	require.NoError(t, queue.Append(context.Background(), correction))
	require.NoError(t, queue.Append(context.Background(), first))
	worker, err := NewEconomicRevisionWorker(queue, store, rater, EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(context.Background()))
	identityFirst, err := first.Identity()
	require.NoError(t, err)
	identityCorrection, err := correction.Identity()
	require.NoError(t, err)
	require.NotEqual(t, identityFirst.Key(), identityCorrection.Key())
	require.NotEqual(t, identityFirst.InputSetHash, identityCorrection.InputSetHash)
	head := store.heads[correction.Queue.String()+"\x00"+correction.HeadKey]
	require.Equal(t, correction.EvidenceRevision, head.EvidenceRevision)
	require.Equal(t, identityCorrection.Key(), head.WorkID)
}

func TestEconomicRevisionWorker_QueueIsolationAndNoBalanceMutation(t *testing.T) {
	t.Parallel()
	queue := &economicRevisionTestQueue{}
	store := newEconomicRevisionTestResultStore()
	customer := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "2")
	provider := economicRevisionTestWork(t, EconomicQueueProvider, 1, "9")
	require.NoError(t, queue.Append(context.Background(), customer))
	require.NoError(t, queue.Append(context.Background(), provider))
	customerRater := &economicRevisionTestRater{}
	providerRater := &economicRevisionTestRater{}
	customerWorker, err := NewEconomicRevisionWorker(queue, store, customerRater, EconomicQueueCustomer, 8)
	require.NoError(t, err)
	providerWorker, err := NewEconomicRevisionWorker(queue, store, providerRater, EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, customerWorker.ProcessOnce(context.Background()))
	require.Equal(t, 0, len(providerRater.calls))
	require.Equal(t, 0, store.balanceLock)
	require.NoError(t, providerWorker.ProcessOnce(context.Background()))
	require.Len(t, customerRater.calls, 1)
	require.Len(t, providerRater.calls, 1)
	require.Equal(t, 0, store.balanceLock)
}

func TestEconomicRevisionWorker_RestartAndReorderedReplayConverge(t *testing.T) {
	t.Parallel()
	queue := &economicRevisionTestQueue{}
	store := newEconomicRevisionTestResultStore()
	first := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "1")
	second := economicRevisionTestWork(t, EconomicQueueCustomer, 2, "2")
	require.NoError(t, queue.Append(context.Background(), second))
	require.NoError(t, queue.Append(context.Background(), first))
	firstRater := &economicRevisionTestRater{}
	worker, err := NewEconomicRevisionWorker(queue, store, firstRater, EconomicQueueCustomer, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(context.Background()))
	secondIdentity, err := second.Identity()
	require.NoError(t, err)
	head := store.heads[second.Queue.String()+"\x00"+second.HeadKey]
	require.Equal(t, secondIdentity.Key(), head.WorkID)

	// A fresh worker over the same durable queue must not re-rate either
	// revision, and processing the older revision after the newer one cannot
	// regress the deterministic head.
	restartedRater := &economicRevisionTestRater{}
	restarted, err := NewEconomicRevisionWorker(queue, store, restartedRater, EconomicQueueCustomer, 8)
	require.NoError(t, err)
	require.NoError(t, restarted.ProcessOnce(context.Background()))
	require.Empty(t, restartedRater.calls)
	require.Equal(t, secondIdentity.Key(), store.heads[second.Queue.String()+"\x00"+second.HeadKey].WorkID)
}

type economicRevisionInputMismatchRetry struct {
	work   EconomicRevisionWork
	claim  EconomicRevisionWorkClaim
	reason string
}

type economicRevisionInputMismatchQueue struct {
	mu        sync.Mutex
	work      EconomicRevisionWork
	claims    []EconomicRevisionWorkClaim
	retries   []economicRevisionInputMismatchRetry
	completes []EconomicRevisionWorkClaim
}

func (q *economicRevisionInputMismatchQueue) ListPendingEconomicRevisionWork(_ context.Context, queue EconomicQueue, _ int) ([]EconomicRevisionWork, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.work.Queue != queue {
		return nil, nil
	}
	return []EconomicRevisionWork{q.work}, nil
}

func (q *economicRevisionInputMismatchQueue) ClaimEconomicRevisionWork(_ context.Context, work EconomicRevisionWork, owner string, lease time.Duration) (EconomicRevisionWorkClaim, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	claim := EconomicRevisionWorkClaim{Owner: owner, Fence: 1, LeaseUntil: time.Now().Add(lease)}
	q.claims = append(q.claims, claim)
	q.work = work
	return claim, true, nil
}

func (q *economicRevisionInputMismatchQueue) RetryEconomicRevisionWork(_ context.Context, work EconomicRevisionWork, claim EconomicRevisionWorkClaim, reason string, _ time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.retries = append(q.retries, economicRevisionInputMismatchRetry{work: work, claim: claim, reason: reason})
	return nil
}

func (q *economicRevisionInputMismatchQueue) CompleteEconomicRevisionWork(_ context.Context, _ EconomicRevisionWork, claim EconomicRevisionWorkClaim) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completes = append(q.completes, claim)
	return nil
}

type economicRevisionPermissiveResultStore struct {
	mu      sync.Mutex
	results []EconomicRevisionResult
}

func (s *economicRevisionPermissiveResultStore) AppendEconomicRevisionResult(_ context.Context, _ EconomicRevisionWork, result EconomicRevisionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, result)
	return nil
}

type economicRevisionMismatchedValuationRater struct{}

func (economicRevisionMismatchedValuationRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	refs := append([]metering.ObservationRef(nil), input.ObservationRefs...)
	if len(refs) == 0 {
		return economics.Valuation{}, errors.New("test rater requires canonical input references")
	}
	refs[0].ObservationID += "-different-input"
	inputSetHash, err := economics.CanonicalInputSetHash(input.Basis, refs)
	if err != nil {
		return economics.Valuation{}, err
	}
	return economics.Valuation{
		ID:                "mismatched-rater-id",
		Version:           economics.ValuationVersionV2,
		Perspective:       input.Perspective,
		Basis:             input.Basis,
		Subject:           input.Subject,
		Scope:             input.Scope,
		InputObservations: refs,
		InputSetHash:      inputSetHash,
		Completeness:      economics.CompletenessPartial,
		CreatedAt:         time.Unix(1_700_030_000, 0).UTC(),
	}, nil
}

func TestEconomicRevisionWorker_RejectsMismatchedValuationInputBeforeReconcileOrPersist(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "6")
	normalizedWork, err := work.Normalize()
	require.NoError(t, err)

	queue := &economicRevisionInputMismatchQueue{work: work}
	store := &economicRevisionPermissiveResultStore{}
	reconciler := &economicRevisionTestReconciler{}
	worker, err := NewEconomicRevisionWorkerWithReconciler(queue, store, economicRevisionMismatchedValuationRater{}, reconciler, EconomicQueueCustomer, 1)
	require.NoError(t, err)

	err = worker.ProcessOnce(context.Background())
	require.ErrorIs(t, err, ErrEconomicRevisionInputMismatch)
	var mismatch *EconomicRevisionInputMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, normalizedWork.InputSetHash, mismatch.Expected)
	require.NotEqual(t, mismatch.Expected, mismatch.Actual)
	require.Empty(t, reconciler.calls, "mismatched valuation must be rejected before reconciliation")
	require.Empty(t, store.results, "mismatched valuation must be rejected before persistence")
	require.Len(t, queue.claims, 1)
	require.Len(t, queue.retries, 1, "rejected valuation must release work for retry")
	require.Empty(t, queue.completes, "rejected valuation must not complete work")
	require.Equal(t, normalizedWork.InputSetHash, queue.retries[0].work.InputSetHash)
	require.Contains(t, queue.retries[0].reason, "normalize valuation")
}

type economicRevisionEmptyValuationRater struct{}

func (economicRevisionEmptyValuationRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	return economics.Valuation{
		ID:           "empty-rater-id",
		Version:      economics.ValuationVersionV2,
		Perspective:  input.Perspective,
		Basis:        input.Basis,
		Subject:      input.Subject,
		Scope:        input.Scope,
		Completeness: economics.CompletenessPartial,
		CreatedAt:    time.Unix(1_700_030_001, 0).UTC(),
	}, nil
}

func TestEconomicRevisionWorker_RejectsEmptyValuationInputReferences(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "7")
	normalizedWork, err := work.Normalize()
	require.NoError(t, err)

	queue := &economicRevisionInputMismatchQueue{work: work}
	store := &economicRevisionPermissiveResultStore{}
	reconciler := &economicRevisionTestReconciler{}
	worker, err := NewEconomicRevisionWorkerWithReconciler(queue, store, economicRevisionEmptyValuationRater{}, reconciler, EconomicQueueCustomer, 1)
	require.NoError(t, err)

	err = worker.ProcessOnce(context.Background())
	require.ErrorIs(t, err, ErrEconomicRevisionInputMismatch)
	var mismatch *EconomicRevisionInputMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, normalizedWork.InputSetHash, mismatch.Expected)
	require.NotEqual(t, mismatch.Expected, mismatch.Actual)
	require.Empty(t, reconciler.calls, "empty valuation references must be rejected before reconciliation")
	require.Empty(t, store.results, "empty valuation references must be rejected before persistence")
	require.Len(t, queue.retries, 1, "rejected valuation must release work for retry")
	require.Empty(t, queue.completes, "rejected valuation must not complete work")
}

func TestEconomicRevisionWorker_RetryConvergesAcrossDerivedTimestamps(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 3, "11")
	queue := &economicRevisionInputMismatchQueue{work: work}
	store := &economicRevisionReplayResultStore{failAfterFirst: true}
	rater := &economicRevisionTimestampRater{}
	reconciler := &economicRevisionTimestampReconciler{}
	worker, err := NewEconomicRevisionWorkerWithReconciler(queue, store, rater, reconciler, EconomicQueueCustomer, 1)
	require.NoError(t, err)

	// The first append is durably recorded but reports an ambiguous failure;
	// retrying the same immutable work must converge despite fresh wall-clock
	// metadata from both pure calculators.
	require.Error(t, worker.ProcessOnce(context.Background()))
	require.NoError(t, worker.ProcessOnce(context.Background()))
	require.Equal(t, 2, rater.calls)
	require.Equal(t, 2, reconciler.calls)
	require.Len(t, store.payloads, 2)
	require.Equal(t, string(store.payloads[0]), string(store.payloads[1]))
	require.Len(t, store.results, 2)
	require.Equal(t, work.CreatedAt, store.results[0].Valuation.CreatedAt)
	require.Equal(t, work.CreatedAt, store.results[1].Valuation.CreatedAt)
	require.NotNil(t, store.results[0].Reconciliation)
	require.NotNil(t, store.results[1].Reconciliation)
	require.Equal(t, work.CreatedAt, store.results[0].Reconciliation.CreatedAt)
	require.Equal(t, work.CreatedAt, store.results[1].Reconciliation.CreatedAt)
}

func TestEconomicRevisionWorker_DuplicateWorkersConvergeAcrossDerivedTimestamps(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 4, "12")
	queue := &economicRevisionInputMismatchQueue{work: work}
	store := &economicRevisionReplayResultStore{}
	rater := &economicRevisionTimestampRater{}
	reconciler := &economicRevisionTimestampReconciler{}
	first, err := NewEconomicRevisionWorkerWithReconciler(queue, store, rater, reconciler, EconomicQueueProvider, 1)
	require.NoError(t, err)
	second, err := NewEconomicRevisionWorkerWithReconciler(queue, store, rater, reconciler, EconomicQueueProvider, 1)
	require.NoError(t, err)

	// The queue deliberately presents the same pending marker to both workers;
	// the durable result identity fence must make duplicate processing a no-op.
	require.NoError(t, first.ProcessOnce(context.Background()))
	require.NoError(t, second.ProcessOnce(context.Background()))
	require.Equal(t, 2, rater.calls)
	require.Equal(t, 2, reconciler.calls)
	require.Len(t, store.payloads, 2)
	require.Equal(t, string(store.payloads[0]), string(store.payloads[1]))
	require.Equal(t, work.CreatedAt, store.results[0].Valuation.CreatedAt)
	require.Equal(t, work.CreatedAt, store.results[1].Valuation.CreatedAt)
	require.NotNil(t, store.results[0].Reconciliation)
	require.NotNil(t, store.results[1].Reconciliation)
	require.Equal(t, work.CreatedAt, store.results[0].Reconciliation.CreatedAt)
	require.Equal(t, work.CreatedAt, store.results[1].Reconciliation.CreatedAt)
}
