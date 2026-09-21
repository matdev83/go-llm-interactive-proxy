package billingstore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 seventh-pass residual closure: a correction that changes only the
// canonical allocation coverage refs, retaining the same observation set,
// subject and pricing context, must be a distinct immutable economic revision
// that advances the selected head. The observation plane stays independently
// fenced by the observation-only work hash; the full allocation-aware identity
// distinguishes the two revisions.

// seventhPassEchoAllocationRater echoes the allocation coverage refs declared by
// the immutable work input, exactly as an allocation-aware production rater
// prices the declared allocation input set.
type seventhPassEchoAllocationRater struct {
	mu    sync.Mutex
	calls int
}

func (r *seventhPassEchoAllocationRater) Rate(_ context.Context, input economics.PostUsageRatingInput) (economics.Valuation, error) {
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
	return economics.Valuation{
		ID: "seventhpass-echo-rater-id", Version: economics.ValuationVersionV2,
		Perspective: input.Perspective, Basis: input.Basis, Subject: input.Subject,
		Scope: input.Scope, InputObservations: refs,
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), input.AllocationCoverageRefs...),
		Completeness:           economics.CompletenessPartial,
		CreatedAt:              time.Unix(1_700_400_000, 0).UTC(),
	}, nil
}

func seventhPassReplacementWork(t *testing.T, observation metering.Observation, allocations []economics.AllocationRef, headKey string, createdAt time.Time) billing.EconomicRevisionWork {
	t.Helper()
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Observations:           []metering.Observation{observation.Clone()},
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), allocations...),
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: observation.Subject,
		EvidenceRevision: observation.Revision, Input: input, CreatedAt: createdAt,
	}
}

func seventhPassReplacementAllocations(storeID string) (economics.AllocationRef, economics.AllocationRef) {
	return economics.AllocationRef{StoreID: storeID, AllocationID: "alloc-replacement", Version: 1, PayloadHash: strings.Repeat("1", 64)},
		economics.AllocationRef{StoreID: storeID, AllocationID: "alloc-replacement", Version: 2, PayloadHash: strings.Repeat("2", 64)}
}

func seventhPassAssertReplacementState(t *testing.T, store *DurableStore, first, second billing.EconomicRevisionWork, allocV1, allocV2 economics.AllocationRef) {
	t.Helper()
	ctx := context.Background()
	identityFirst, err := first.Identity()
	require.NoError(t, err)
	identitySecond, err := second.Identity()
	require.NoError(t, err)
	require.NotEqual(t, identityFirst.Key(), identitySecond.Key(),
		"an allocation-only correction must be a distinct immutable revision")
	require.NotEqual(t, identityFirst.ValuationKey(), identitySecond.ValuationKey())

	valuationFirst, err := store.GetValuation(ctx, identityFirst.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{allocV1}, valuationFirst.AllocationCoverageRefs)
	valuationSecond, err := store.GetValuation(ctx, identitySecond.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{allocV2}, valuationSecond.AllocationCoverageRefs)
	require.NotEqual(t, valuationFirst.InputSetHash, valuationSecond.InputSetHash,
		"the full allocation-aware identity distinguishes the revisions")

	observationHash, err := economics.CanonicalInputSetHash(first.Input.Basis, valuationSecond.InputObservations)
	require.NoError(t, err)
	require.Equal(t, identityFirst.InputSetHash, observationHash, "the observation plane fence is shared")
	fullHash, err := economics.CanonicalValuationInputSetHash(valuationSecond.Basis, valuationSecond.InputObservations, valuationSecond.AllocationCoverageRefs)
	require.NoError(t, err)
	require.Equal(t, fullHash, valuationSecond.InputSetHash)

	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, first.HeadKey)
	require.NoError(t, err)
	require.Equal(t, identitySecond.ValuationKey(), head.ValuationID,
		"the authoritative head must advance to the replacement revision")
	require.Equal(t, identitySecond.Key(), head.WorkID)

	frozen, err := store.detailSelectedValuations(ctx, first.Subject.TenantID, []billing.SelectedCostValuationRef{{
		ValuationID: identitySecond.ValuationKey(), Revision: 1, InputSetHash: valuationSecond.InputSetHash,
	}})
	require.NoError(t, err)
	require.Len(t, frozen, 1, "the replacement frozen selected identity must reload")
	require.Equal(t, []economics.AllocationRef{allocV2}, frozen[0].AllocationCoverageRefs)

	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuationCount))
	require.Equal(t, 2, valuationCount, "both immutable revisions must persist")
}

func TestPhase16SeventhPassWorkerAllocationOnlyReplacementAdvances(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "seventhpass-replacement", 51)
	allocV1, allocV2 := seventhPassReplacementAllocations(store.StoreID())
	headKey := "seventhpass-replacement-head"

	first := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_200_000, 0).UTC())
	second := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV2}, headKey, time.Unix(1_700_200_100, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second), "a distinct allocation derivation is a distinct queue item")

	rater := &seventhPassEchoAllocationRater{}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx), "both allocation-aware revisions must persist through the worker")
	require.Equal(t, 2, rater.calls)

	seventhPassAssertReplacementState(t, store, first, second, allocV1, allocV2)

	// Exact replay of both immutable markers is idempotent.
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls, "durably completed revisions must not re-rate")
}

func TestPhase16SeventhPassJobRunnerAllocationOnlyReplacementAdvances(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "seventhpass-replacement-runner", 52)
	allocV1, allocV2 := seventhPassReplacementAllocations(store.StoreID())
	headKey := "seventhpass-replacement-runner-head"

	first := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_210_000, 0).UTC())
	second := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV2}, headKey, time.Unix(1_700_210_100, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))

	rater := &seventhPassEchoAllocationRater{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, &economicJobRunnerStoreReconciler{}))
	require.NoError(t, err)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 2, summary.Completed)
	require.Zero(t, summary.Failed)
	require.Zero(t, summary.Retried)
	require.Equal(t, 2, rater.calls)

	seventhPassAssertReplacementState(t, store, first, second, allocV1, allocV2)
}

func TestPhase16SeventhPassAllocationReorderingIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "seventhpass-allocation-reorder", 54)
	allocA := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-reorder-a", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocB := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-reorder-b", Version: 1, PayloadHash: strings.Repeat("b", 64)}
	headKey := "seventhpass-reorder-head"
	ordered := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocA, allocB}, headKey, time.Unix(1_700_230_000, 0).UTC())
	reordered := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocB, allocA}, headKey, time.Unix(1_700_230_000, 0).UTC())
	orderedIdentity, err := ordered.Identity()
	require.NoError(t, err)
	reorderedIdentity, err := reordered.Identity()
	require.NoError(t, err)
	require.Equal(t, orderedIdentity.Key(), reorderedIdentity.Key(),
		"allocation ref order must not change the derivation identity")
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, ordered))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reordered), "a reordered identical set is the same queue item")

	rater := &seventhPassEchoAllocationRater{}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 1, rater.calls, "a reordered identical allocation set must not produce a second revision")

	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuationCount))
	require.Equal(t, 1, valuationCount)
}

func TestPhase16SeventhPassAllocationClaimTamperRejected(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "seventhpass-claim-tamper", 53)
	allocV1, allocV2 := seventhPassReplacementAllocations(store.StoreID())

	work := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, "seventhpass-claim-tamper-head", time.Unix(1_700_220_000, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, work))
	identity, err := work.Identity()
	require.NoError(t, err)

	// The rater returns a different allocation revision than the work declared.
	rater := &seventhPassAllocationRater{allocations: []economics.AllocationRef{allocV2}}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 4)
	require.NoError(t, err)
	err = worker.ProcessOnce(ctx)
	require.ErrorIs(t, err, billing.ErrEconomicRevisionInputMismatch,
		"a rater may not substitute an unclaimed allocation revision")

	_, getErr := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.Error(t, getErr, "a tampered allocation claim must not persist")
}
