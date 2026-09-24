package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 eighth-pass Finding 1 RED contract: allocation-aware selected head
// ordering is nonmonotone and equal timestamps are nondeterministic.
//
// Defect under test (economic_revision_store.go appendEconomicValuationHeadInTx):
// same observation plane compares incoming CreatedAt > existing CreatedAt, but
// head UPDATE advances updated_at only, not created_at. Sequence t1 -> t3 ->
// delayed t2 compares delayed t2 against stored t1 and regresses head. Equal
// timestamps keep first arrival without canonical tie-break.
//
// Required ordering (remediation contract):
//   - durable current winning ordering tuple (winning CreatedAt + full
//     derivation identity) decides advancement, never the initial created_at
//     nor arrival order;
//   - primary key is winning CreatedAt (larger wins, smaller never regresses);
//   - equal-time tie-break is total and deterministic (derivation/full identity
//     lexical, larger wins), consistent SQLite/PG;
//   - exact replay is idempotent; older revision cannot advance; later valid
//     derivation advances; observation-only legacy and immutable history stay.

func finding1HeadOrderingWork(t *testing.T, observation metering.Observation, allocations []economics.AllocationRef, headKey string, createdAt time.Time, evidenceRevision uint64) billing.EconomicRevisionWork {
	t.Helper()
	revision := observation.Revision
	if evidenceRevision != 0 {
		revision = evidenceRevision
	}
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Observations:           []metering.Observation{observation.Clone()},
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), allocations...),
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: observation.Subject,
		EvidenceRevision: revision, Input: input, CreatedAt: createdAt,
	}
}

func finding1HeadOrderingAlloc(storeID, id string, version uint64, fill string) economics.AllocationRef {
	return economics.AllocationRef{StoreID: storeID, AllocationID: id, Version: version, PayloadHash: strings.Repeat(fill, 64)}
}

func finding1HeadOrderingProcess(t *testing.T, store *DurableStore, rater *seventhPassEchoAllocationRater) {
	t.Helper()
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(context.Background()))
}

func finding1HeadWorkID(t *testing.T, work billing.EconomicRevisionWork) string {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	return identity.Key()
}

func finding1HeadValuationKey(t *testing.T, work billing.EconomicRevisionWork) string {
	t.Helper()
	identity, err := work.Identity()
	require.NoError(t, err)
	return identity.ValuationKey()
}

// t1 -> t3 -> delayed t2 must not regress the authoritative head.
func TestPhase16Finding1HeadOrderingDelayedT2NoRegression(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "finding1-head-ordering-t1t3t2", 81)
	headKey := "finding1-head-ordering-t1t3t2"

	allocV1 := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-order", 1, "1")
	allocV2 := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-order", 2, "2")
	allocV3 := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-order", 3, "3")

	t1 := time.Unix(1_700_500_000, 0).UTC()
	t2 := t1.Add(50 * time.Second)
	t3 := t1.Add(100 * time.Second)

	w1 := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocV1}, headKey, t1, 0)
	w3 := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocV3}, headKey, t3, 0)
	w2 := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocV2}, headKey, t2, 0)

	rater := &seventhPassEchoAllocationRater{}
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w1))
	finding1HeadOrderingProcess(t, store, rater)
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, finding1HeadWorkID(t, w1), head.WorkID, "t1 must become head")

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w3))
	finding1HeadOrderingProcess(t, store, rater)
	head, err = store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, finding1HeadWorkID(t, w3), head.WorkID, "t3 correction must advance head")
	require.Equal(t, finding1HeadValuationKey(t, w3), head.ValuationID)

	// Delayed t2 arrives after t3 was already selected.
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w2))
	finding1HeadOrderingProcess(t, store, rater)
	head, err = store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, finding1HeadWorkID(t, w3), head.WorkID, "delayed t2 must not regress t3 head")
	require.Equal(t, finding1HeadValuationKey(t, w3), head.ValuationID)

	// Immutable history must retain all three derivations.
	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuationCount))
	require.Equal(t, 3, valuationCount, "all immutable valuations must persist")

	// Exact replay of the whole sequence is idempotent.
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w1))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w3))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w2))
	beforeCalls := rater.calls
	finding1HeadOrderingProcess(t, store, rater)
	require.Equal(t, beforeCalls, rater.calls, "exact replay must not re-rate")
	headAfter, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, head.WorkID, headAfter.WorkID)
	require.Equal(t, head.ValuationID, headAfter.ValuationID)
}

// Equal timestamps for distinct derivations must converge independent of arrival order.
func TestPhase16Finding1HeadOrderingEqualTimestampDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	equalAt := time.Unix(1_700_510_000, 0).UTC()

	buildPair := func(t *testing.T, store *DurableStore, headKey string) (billing.EconomicRevisionWork, billing.EconomicRevisionWork) {
		t.Helper()
		observation := phase4EconomicsObservation("test", "finding1-head-ordering-equal", 82)
		allocA := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-equal", 1, "a")
		allocB := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-equal", 2, "b")
		// Same observation plane, same evidence revision, same CreatedAt, distinct derivations.
		wA := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocA}, headKey, equalAt, 0)
		wB := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocB}, headKey, equalAt, 0)
		idA, err := wA.Identity()
		require.NoError(t, err)
		idB, err := wB.Identity()
		require.NoError(t, err)
		require.NotEqual(t, idA.Key(), idB.Key())
		require.NotEqual(t, idA.DerivationHash, idB.DerivationHash)
		return wA, wB
	}
	expectedWinner := func(t *testing.T, wA, wB billing.EconomicRevisionWork) string {
		t.Helper()
		idA, err := wA.Identity()
		require.NoError(t, err)
		idB, err := wB.Identity()
		require.NoError(t, err)
		// Total deterministic tie-break: larger full identity wins (consistent with
		// same-revision equal-evidence Less fallback). Never arrival order.
		if idA.Less(idB) {
			return idB.Key()
		}
		return idA.Key()
	}

	forwardStore := newSQLiteTestStore(t)
	forwardKey := "finding1-head-equal-forward"
	fwA, fwB := buildPair(t, forwardStore, forwardKey)
	want := expectedWinner(t, fwA, fwB)
	require.NoError(t, forwardStore.AppendEconomicRevisionWork(ctx, fwA))
	require.NoError(t, forwardStore.AppendEconomicRevisionWork(ctx, fwB))
	finding1HeadOrderingProcess(t, forwardStore, &seventhPassEchoAllocationRater{})
	forwardHead, err := forwardStore.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, forwardKey)
	require.NoError(t, err)

	reverseStore := newSQLiteTestStore(t)
	reverseKey := "finding1-head-equal-reverse"
	// Same logical derivations, opposite arrival order. Use identical allocation
	// identities so WorkIDs match across stores (StoreID is "test" for both).
	rvA, rvB := buildPair(t, reverseStore, forwardKey)
	_ = reverseKey
	require.NoError(t, reverseStore.AppendEconomicRevisionWork(ctx, rvB))
	require.NoError(t, reverseStore.AppendEconomicRevisionWork(ctx, rvA))
	finding1HeadOrderingProcess(t, reverseStore, &seventhPassEchoAllocationRater{})
	reverseHead, err := reverseStore.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, forwardKey)
	require.NoError(t, err)

	require.Equal(t, want, forwardHead.WorkID, "equal-time forward order must select deterministic winner")
	require.Equal(t, want, reverseHead.WorkID, "equal-time reverse order must select same deterministic winner")
	require.Equal(t, forwardHead.WorkID, reverseHead.WorkID, "equal timestamps must converge independent of arrival order")

	var forwardCount int
	require.NoError(t, forwardStore.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, forwardStore.StoreID()).Scan(ctx, &forwardCount))
	require.Equal(t, 2, forwardCount, "both equal-time derivations must persist as immutable history")
}

// Normalized-zero timestamps tie and must use the same deterministic tie-break.
func TestPhase16Finding1HeadOrderingZeroTimestampDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storeForward := newSQLiteTestStore(t)
	storeReverse := newSQLiteTestStore(t)
	headKey := "finding1-head-zero-time"

	buildZeroPair := func(t *testing.T, store *DurableStore) (billing.EconomicRevisionWork, billing.EconomicRevisionWork) {
		t.Helper()
		observation := phase4EconomicsObservation("test", "finding1-head-ordering-zero", 83)
		allocA := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-zero", 1, "a")
		allocB := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-zero", 2, "b")
		wA := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocA}, headKey, time.Time{}, 0)
		wB := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocB}, headKey, time.Time{}, 0)
		return wA, wB
	}

	fwA, fwB := buildZeroPair(t, storeForward)
	idA, err := fwA.Identity()
	require.NoError(t, err)
	idB, err := fwB.Identity()
	require.NoError(t, err)
	require.NotEqual(t, idA.Key(), idB.Key())
	want := idB.Key()
	if !idA.Less(idB) {
		want = idA.Key()
	}
	require.NoError(t, storeForward.AppendEconomicRevisionWork(ctx, fwA))
	require.NoError(t, storeForward.AppendEconomicRevisionWork(ctx, fwB))
	finding1HeadOrderingProcess(t, storeForward, &seventhPassEchoAllocationRater{})
	forwardHead, err := storeForward.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)

	rvA, rvB := buildZeroPair(t, storeReverse)
	require.NoError(t, storeReverse.AppendEconomicRevisionWork(ctx, rvB))
	require.NoError(t, storeReverse.AppendEconomicRevisionWork(ctx, rvA))
	finding1HeadOrderingProcess(t, storeReverse, &seventhPassEchoAllocationRater{})
	reverseHead, err := storeReverse.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)

	require.Equal(t, want, forwardHead.WorkID)
	require.Equal(t, want, reverseHead.WorkID)
	require.Equal(t, forwardHead.WorkID, reverseHead.WorkID, "zero timestamps must converge independent of arrival order")
}

// Older evidence revision cannot advance; later valid derivation advances.
func TestPhase16Finding1HeadOrderingEvidenceRevisionFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	olderObservation := phase4EconomicsObservation("test", "finding1-head-ordering-revision-older", 91)
	newerObservation := phase4EconomicsObservation("test", "finding1-head-ordering-revision-newer", 92)
	headKey := "finding1-head-ordering-revision"
	alloc := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-revision", 1, "c")

	older := finding1HeadOrderingWork(t, olderObservation, []economics.AllocationRef{alloc}, headKey, time.Unix(1_700_520_000, 0).UTC(), 91)
	newer := finding1HeadOrderingWork(t, newerObservation, []economics.AllocationRef{alloc}, headKey, time.Unix(1_700_520_100, 0).UTC(), 92)
	olderID, err := older.Identity()
	require.NoError(t, err)
	newerID, err := newer.Identity()
	require.NoError(t, err)
	require.NotEqual(t, olderID.Key(), newerID.Key())

	rater := &seventhPassEchoAllocationRater{}
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, older))
	finding1HeadOrderingProcess(t, store, rater)
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, olderID.Key(), head.WorkID)

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, newer))
	finding1HeadOrderingProcess(t, store, rater)
	head, err = store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, newerID.Key(), head.WorkID, "later evidence revision must advance")

	// Replaying the older revision after the newer head must not regress.
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, older))
	finding1HeadOrderingProcess(t, store, rater)
	head, err = store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, newerID.Key(), head.WorkID, "older evidence revision must not regress newer head")

	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuationCount))
	require.Equal(t, 2, valuationCount, "both revision derivations must persist")
}

// Observation-only legacy behavior: same observation plane without allocations
// is one immutable identity; replay is idempotent and head is stable.
func TestPhase16Finding1HeadOrderingLegacyObservationOnlyStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteTestStore(t)
	observation := phase4EconomicsObservation("test", "finding1-head-ordering-legacy", 93)
	headKey := "finding1-head-ordering-legacy"

	first := finding1HeadOrderingWork(t, observation, nil, headKey, time.Unix(1_700_530_000, 0).UTC(), 0)
	second := finding1HeadOrderingWork(t, observation, nil, headKey, time.Unix(1_700_530_100, 0).UTC(), 0)
	firstID, err := first.Identity()
	require.NoError(t, err)
	secondID, err := second.Identity()
	require.NoError(t, err)
	require.Equal(t, firstID.Key(), secondID.Key(), "observation-only re-runs share one immutable identity")
	require.Empty(t, firstID.DerivationHash)

	rater := &seventhPassEchoAllocationRater{}
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second), "same observation-only identity replays as same queue item")
	finding1HeadOrderingProcess(t, store, rater)
	require.Equal(t, 1, rater.calls, "legacy observation-only work must rate once")
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, firstID.Key(), head.WorkID)
	require.Equal(t, firstID.ValuationKey(), head.ValuationID)

	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuationCount))
	require.Equal(t, 1, valuationCount)
}
