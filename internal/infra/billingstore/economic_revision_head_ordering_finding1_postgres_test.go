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

// Phase 16 eighth-pass Finding 1 PostgreSQL RED contract: same durable
// selected-head ordering tuple and deterministic equal-time tie-break must hold
// on configured PostgreSQL, not only SQLite.

func TestPhase16Finding1HeadOrderingDelayedT2NoRegressionPostgres(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	observation := phase4EconomicsObservation("test", "finding1-head-ordering-pg-t1t3t2", 81)
	headKey := "finding1-head-ordering-pg-t1t3t2"
	allocV1 := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-order-pg", 1, "1")
	allocV2 := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-order-pg", 2, "2")
	allocV3 := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-order-pg", 3, "3")

	t1 := time.Unix(1_700_500_000, 0).UTC()
	t2 := t1.Add(50 * time.Second)
	t3 := t1.Add(100 * time.Second)

	w1 := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocV1}, headKey, t1, 0)
	w3 := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocV3}, headKey, t3, 0)
	w2 := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocV2}, headKey, t2, 0)

	rater := &seventhPassEchoAllocationRater{}
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w1))
	finding1HeadOrderingProcess(t, store, rater)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w3))
	finding1HeadOrderingProcess(t, store, rater)
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, finding1HeadWorkID(t, w3), head.WorkID)

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, w2))
	finding1HeadOrderingProcess(t, store, rater)
	head, err = store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, finding1HeadWorkID(t, w3), head.WorkID, "delayed t2 must not regress t3 head on PostgreSQL")
	require.Equal(t, finding1HeadValuationKey(t, w3), head.ValuationID)

	var valuationCount int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_valuations WHERE store_id = ?`, store.StoreID()).Scan(ctx, &valuationCount))
	require.Equal(t, 3, valuationCount)
}

func TestPhase16Finding1HeadOrderingEqualTimestampDeterministicPostgres(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	bunDB2, _ := openIsolatedPostgresBun(t, dsn, 4)
	storeReverse, err := NewDurableStore(ctx, bunDB2, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeReverse.Close() })
	require.NoError(t, VerifySchema(ctx, storeReverse.db))

	equalAt := time.Unix(1_700_510_000, 0).UTC()
	headKey := "finding1-head-equal-pg"

	observation := phase4EconomicsObservation("test", "finding1-head-ordering-equal-pg", 82)
	allocA := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-equal-pg", 1, "a")
	allocB := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-equal-pg", 2, "b")
	wA := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocA}, headKey, equalAt, 0)
	wB := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{allocB}, headKey, equalAt, 0)
	idA, err := wA.Identity()
	require.NoError(t, err)
	idB, err := wB.Identity()
	require.NoError(t, err)
	want := idB.Key()
	if !idA.Less(idB) {
		want = idA.Key()
	}

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, wA))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, wB))
	finding1HeadOrderingProcess(t, store, &seventhPassEchoAllocationRater{})
	forwardHead, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)

	// Reverse arrival order must converge to the same deterministic winner.
	observationReverse := phase4EconomicsObservation("test", "finding1-head-ordering-equal-pg", 82)
	allocAReverse := finding1HeadOrderingAlloc(storeReverse.StoreID(), "alloc-head-equal-pg", 1, "a")
	allocBReverse := finding1HeadOrderingAlloc(storeReverse.StoreID(), "alloc-head-equal-pg", 2, "b")
	rwA := finding1HeadOrderingWork(t, observationReverse, []economics.AllocationRef{allocAReverse}, headKey, equalAt, 0)
	rwB := finding1HeadOrderingWork(t, observationReverse, []economics.AllocationRef{allocBReverse}, headKey, equalAt, 0)
	require.NoError(t, storeReverse.AppendEconomicRevisionWork(ctx, rwB))
	require.NoError(t, storeReverse.AppendEconomicRevisionWork(ctx, rwA))
	finding1HeadOrderingProcess(t, storeReverse, &seventhPassEchoAllocationRater{})
	reverseHead, err := storeReverse.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)

	require.Equal(t, want, forwardHead.WorkID)
	require.Equal(t, want, reverseHead.WorkID)
	require.Equal(t, forwardHead.WorkID, reverseHead.WorkID, "PostgreSQL equal timestamps must converge independent of arrival order")
}

func TestPhase16Finding1HeadOrderingEvidenceRevisionFencePostgres(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	observation := phase4EconomicsObservation("test", "finding1-head-ordering-revision-pg-older", 91)
	newerObservation := phase4EconomicsObservation("test", "finding1-head-ordering-revision-pg-newer", 92)
	headKey := "finding1-head-ordering-revision-pg"
	alloc := finding1HeadOrderingAlloc(store.StoreID(), "alloc-head-revision-pg", 1, "c")
	older := finding1HeadOrderingWork(t, observation, []economics.AllocationRef{alloc}, headKey, time.Unix(1_700_520_000, 0).UTC(), 91)
	newer := finding1HeadOrderingWork(t, newerObservation, []economics.AllocationRef{alloc}, headKey, time.Unix(1_700_520_100, 0).UTC(), 92)
	olderID, err := older.Identity()
	require.NoError(t, err)
	newerID, err := newer.Identity()
	require.NoError(t, err)

	rater := &seventhPassEchoAllocationRater{}
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, older))
	finding1HeadOrderingProcess(t, store, rater)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, newer))
	finding1HeadOrderingProcess(t, store, rater)
	head, err := store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, newerID.Key(), head.WorkID)

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, older))
	finding1HeadOrderingProcess(t, store, rater)
	head, err = store.GetEconomicValuationHead(ctx, billing.EconomicQueueProvider, headKey)
	require.NoError(t, err)
	require.Equal(t, newerID.Key(), head.WorkID, "older revision must not regress on PostgreSQL")
	require.Equal(t, olderID.Key(), olderID.Key())
}
