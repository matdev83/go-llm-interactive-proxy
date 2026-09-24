package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// Phase 16 ninth-pass RED: provider-posting recovery must never guess an
// observation-only identity when the result/probe composition hides the exact
// EconomicRevisionValuationLoader and legacy work can persist an
// allocation-bearing output.

// phase16NinthPassNoLoaderResultStore is a representative result+probe
// decorator: it forwards Append/Has to a real store but deliberately hides
// LoadEconomicRevisionValuation.
type phase16NinthPassNoLoaderResultStore struct {
	inner *economicRevisionTestResultStore
}

func (s *phase16NinthPassNoLoaderResultStore) AppendEconomicRevisionResult(ctx context.Context, work EconomicRevisionWork, result EconomicRevisionResult) error {
	return s.inner.AppendEconomicRevisionResult(ctx, work, result)
}

func (s *phase16NinthPassNoLoaderResultStore) HasEconomicRevisionResult(ctx context.Context, identity EconomicRevisionIdentity) (bool, error) {
	return s.inner.HasEconomicRevisionResult(ctx, identity)
}

// phase16NinthPassLegacyAllocationRater prices legacy work that declares no
// allocation refs with a valid allocation-bearing output. The normalizer
// legitimately accepts this on the lenient legacy seam.
type phase16NinthPassLegacyAllocationRater struct {
	mu    sync.Mutex
	calls int
}

func (r *phase16NinthPassLegacyAllocationRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
	r.mu.Lock()
	r.calls++
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
		ID:                "ninthpass-rater-id",
		Version:           economics.ValuationVersionV2,
		Perspective:       input.Perspective,
		Basis:             input.Basis,
		Subject:           input.Subject,
		Scope:             input.Scope,
		InputObservations: refs,
		AllocationCoverageRefs: []economics.AllocationRef{{
			StoreID: input.Subject.StoreID, AllocationID: "phase16-ninthpass-alloc", Version: 1, PayloadHash: strings.Repeat("c", 64),
		}},
		Completeness: economics.CompletenessPartial,
	}, nil
}

func (r *phase16NinthPassLegacyAllocationRater) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// phase16NinthPassLoaderResultStore is the exact-output control: it persists
// the full allocation-aware valuation and serves it back byte-identically.
type phase16NinthPassLoaderResultStore struct {
	mu      sync.Mutex
	results map[string]EconomicRevisionResult
}

func newPhase16NinthPassLoaderResultStore() *phase16NinthPassLoaderResultStore {
	return &phase16NinthPassLoaderResultStore{results: make(map[string]EconomicRevisionResult)}
}

func (s *phase16NinthPassLoaderResultStore) AppendEconomicRevisionResult(_ context.Context, work EconomicRevisionWork, result EconomicRevisionResult) error {
	normalized, err := work.Normalize()
	if err != nil {
		return err
	}
	identity, err := normalized.Identity()
	if err != nil {
		return err
	}
	valuation := result.Valuation.Clone()
	if valuation.ID != identity.ValuationKey() {
		return errors.New("ninthpass loader store requires revision-keyed valuation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.results[identity.Key()]; ok {
		if existing.Valuation.Fingerprint() != valuation.Fingerprint() {
			return ErrEconomicRevisionConflict
		}
		return nil
	}
	s.results[identity.Key()] = EconomicRevisionResult{Valuation: result.Valuation.Clone(), Reconciliation: result.Reconciliation}
	return nil
}

func (s *phase16NinthPassLoaderResultStore) HasEconomicRevisionResult(_ context.Context, identity EconomicRevisionIdentity) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.results[identity.Key()]
	return ok, nil
}

func (s *phase16NinthPassLoaderResultStore) LoadEconomicRevisionValuation(_ context.Context, identity EconomicRevisionIdentity) (economics.Valuation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.results[identity.Key()]
	if !ok {
		return economics.Valuation{}, errors.New("ninthpass loader store: missing persisted valuation")
	}
	return result.Valuation.Clone(), nil
}

func TestPhase16NinthPassNoLoaderProviderPostingRejected(t *testing.T) {
	t.Parallel()
	queue := &economicRevisionTestQueue{}
	inner := newEconomicRevisionTestResultStore()
	wrapper := &phase16NinthPassNoLoaderResultStore{inner: inner}
	poster := &refinement43ProviderCostPoster{}
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	require.Empty(t, identity.DerivationHash, "legacy work must declare no allocation refs")
	require.NoError(t, queue.Append(context.Background(), work))

	_, err = NewEconomicRevisionWorkerWithProviderCost(queue, wrapper, &phase16NinthPassLegacyAllocationRater{}, poster, EconomicQueueProvider, 8)
	require.Error(t, err, "provider-posting worker with a processed-result probe but no exact valuation loader must be rejected before it can persist/post")
}

func TestPhase16NinthPassLoaderBackedLegacyAllocationRetryStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	queue := &economicRevisionTestQueue{}
	store := newPhase16NinthPassLoaderResultStore()
	poster := &refinement43ProviderCostPoster{}
	rater := &phase16NinthPassLegacyAllocationRater{}
	work := refinement43Work(t, 1, "10", metering.PaymentPartyOperator)
	require.NoError(t, queue.Append(ctx, work))
	worker, err := NewEconomicRevisionWorkerWithProviderCost(queue, store, rater, poster, EconomicQueueProvider, 8)
	require.NoError(t, err)

	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, poster.inputs, 1)
	freshKey, err := ProviderCostRevisionSourceKey(poster.inputs[0])
	require.NoError(t, err)
	freshFingerprint, err := poster.inputs[0].SemanticFingerprint()
	require.NoError(t, err)
	require.NotEqual(t, work.InputSetHash, poster.inputs[0].InputSetHash, "allocation-bearing output must use the full valuation hash, not the observation-only work hash")

	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, poster.inputs, 2)
	retryKey, err := ProviderCostRevisionSourceKey(poster.inputs[1])
	require.NoError(t, err)
	retryFingerprint, err := poster.inputs[1].SemanticFingerprint()
	require.NoError(t, err)
	require.Equal(t, freshKey, retryKey, "fresh and recovered provider postings must share the exact source key")
	require.Equal(t, freshFingerprint, retryFingerprint, "fresh and recovered provider postings must share the exact fingerprint")
	require.Equal(t, 1, rater.callCount(), "processed-result probe must avoid re-rating on recovery")
}

func TestPhase16NinthPassLoaderBackedNonpayableExclusionRetryStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	queue := &economicRevisionTestQueue{}
	store := newPhase16NinthPassLoaderResultStore()
	poster := &refinement43ProviderCostPoster{}
	rater := &phase16NinthPassLegacyAllocationRater{}
	work := refinement43Work(t, 1, "12", metering.PaymentPartyCustomer)
	require.NoError(t, queue.Append(ctx, work))
	worker, err := NewEconomicRevisionWorkerWithProviderCost(queue, store, rater, poster, EconomicQueueProvider, 8)
	require.NoError(t, err)

	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, poster.inputs, 1)
	require.False(t, poster.inputs[0].Cost.Payable, "customer-payer revision must post as a known nonpayable exclusion")
	freshKey, err := ProviderCostRevisionSourceKey(poster.inputs[0])
	require.NoError(t, err)

	require.NoError(t, worker.ProcessOnce(ctx))
	require.Len(t, poster.inputs, 2)
	retryKey, err := ProviderCostRevisionSourceKey(poster.inputs[1])
	require.NoError(t, err)
	require.Equal(t, freshKey, retryKey, "nonpayable exclusion recovery must reuse the exact source key, not conflict")
	require.False(t, poster.inputs[1].Cost.Payable)
}

func TestPhase16NinthPassPureWorkerWithoutLoaderRemainsCompatible(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	queue := &economicRevisionTestQueue{}
	inner := newEconomicRevisionTestResultStore()
	wrapper := &phase16NinthPassNoLoaderResultStore{inner: inner}
	work := economicRevisionTestWork(t, EconomicQueueCustomer, 1, "3")
	require.NoError(t, queue.Append(ctx, work))
	worker, err := NewEconomicRevisionWorker(queue, wrapper, &economicRevisionTestRater{}, EconomicQueueCustomer, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.NoError(t, worker.ProcessOnce(ctx), "pure workers without provider posting must stay compatible without a loader")
}
