package billingstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Finding 5B3 RED contract: allocation discovery, exact envelope load and the
// rollup that follows are bounded before materialization. Discovery must carry
// a database-side LIMIT against one global distinct-envelope budget, exact
// cross-chunk deduplication must not consume that budget, the cumulative
// potential line output must be checked before any rollup slice grows, and the
// Finding 2 scope isolation plus conservation lineage must survive untouched.

const edAllocTestTenant = "tenant-ed"

// edTestAllocationQuantityEnvelope builds one canonical conserved allocation
// envelope with an exact non-monetary quantity source, so per-target money
// rounding can never mask a materialization bound probe.
func edTestAllocationQuantityEnvelope(t *testing.T, store *DurableStore, id, accountID string, targets []economics.AllocationTarget) economics.AllocationRecord {
	t.Helper()
	quantity, err := metering.ParseDecimal("10")
	require.NoError(t, err)
	record := economics.AllocationRecord{
		ID: id, Version: 1,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: store.StoreID(), TenantID: edAllocTestTenant, AccountID: accountID, ResourceID: "res-" + id, PeriodID: "2026-09"},
		SourceBasis:   economics.BasisAllocatedCost, SourceQuantity: &quantity, Unit: "unit",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: targets, CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, record.Validate())
	return record
}

// edTestAllocationBLegTarget names one in-store, same-account B-leg target.
func edTestAllocationBLegTarget(store *DurableStore, accountID, callID, bLegID string) economics.AllocationTarget {
	return economics.AllocationTarget{
		TargetID: "t-" + bLegID,
		Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, accountID, "a-ed-alloc-bounds", callID, bLegID),
		Weight:   economics.AllocationFraction{Numerator: "1", Denominator: "1"},
	}
}

// edTestAllocationCallTarget names one in-store, same-account billing-call
// target so a chunk probe can key scope membership on the call list.
func edTestAllocationCallTarget(store *DurableStore, accountID, callID string) economics.AllocationTarget {
	return economics.AllocationTarget{
		TargetID: "t-call-" + callID,
		Target: metering.SubjectRef{
			Kind: metering.SubjectBillingCall, StoreID: store.StoreID(), TenantID: edAllocTestTenant,
			AccountID: accountID, ALegID: "a-ed-alloc-bounds", BillingCallID: callID,
		},
		Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"},
	}
}

func edTestAllocationQuery(store *DurableStore, accountID string) billing.EconomicDetailQuery {
	return billing.EconomicDetailQuery{StoreID: store.StoreID(), AccountID: accountID, ALegID: "a-ed-alloc-bounds"}
}

// TestEconomicDetailAllocationDiscoveryQueryCarriesDatabaseSideLimit proves
// the discovery SQL itself is bounded database-side; an IN-list bound is not a
// row bound and the adapter must not read every matching target before
// rejecting.
func TestEconomicDetailAllocationDiscoveryQueryCarriesDatabaseSideLimit(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-limit", "USD")
	record := edTestAllocationQuantityEnvelope(t, store, "alloc-ed-limit", account.ID,
		[]economics.AllocationTarget{edTestAllocationBLegTarget(store, account.ID, "call-ed-limit", "b-ed-limit")})
	require.NoError(t, store.AppendAllocation(ctx, record))

	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-limit"}, []string{"b-ed-limit"})
	require.NoError(t, err)
	require.Len(t, got, 1)

	bounded := false
	for _, query := range recorder.queries {
		if strings.Contains(query, "billing_allocation_targets") && strings.Contains(query, "LIMIT") {
			bounded = true
		}
	}
	require.True(t, bounded, "allocation discovery SQL must carry a database-side LIMIT; recorded: %v", recorder.queries)
}

// TestEconomicDetailAllocationEnvelopeBoundFailsClosed proves a scope with more
// distinct envelopes than the finite budget fails closed instead of
// materializing the over-bound discovery set.
func TestEconomicDetailAllocationEnvelopeBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-over", "USD")
	for i := 0; i <= economicDetailMaxAllocationRecords; i++ {
		record := edTestAllocationQuantityEnvelope(t, store, fmt.Sprintf("alloc-ed-over-%03d", i), account.ID,
			[]economics.AllocationTarget{edTestAllocationBLegTarget(store, account.ID, "call-ed-over", "b-ed-over")})
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-over"}, []string{"b-ed-over"})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got, "an over-bound allocation set must never be retained")
}

// TestEconomicDetailAllocationEnvelopeBoundaryPasses proves exactly the finite
// envelope budget is accepted; the bound fails over, not at, the maximum.
func TestEconomicDetailAllocationEnvelopeBoundaryPasses(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-max", "USD")
	for i := 0; i < economicDetailMaxAllocationRecords; i++ {
		record := edTestAllocationQuantityEnvelope(t, store, fmt.Sprintf("alloc-ed-max-%03d", i), account.ID,
			[]economics.AllocationTarget{edTestAllocationBLegTarget(store, account.ID, "call-ed-max", "b-ed-max")})
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-max"}, []string{"b-ed-max"})
	require.NoError(t, err)
	require.Len(t, got, economicDetailMaxAllocationRecords)
}

// TestEconomicDetailAllocationLineBudgetFailsBeforeRollup proves the cumulative
// potential line output of the loaded envelopes is checked before any rollup
// slice grows: one envelope one target over the detail allocation line bound
// fails closed even though only a single line is in scope.
func TestEconomicDetailAllocationLineBudgetFailsBeforeRollup(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-lines-over", "USD")

	total := billing.MaxEconomicDetailAllocations + 1
	targets := make([]economics.AllocationTarget, 0, total)
	targets = append(targets, edTestAllocationBLegTarget(store, account.ID, "call-ed-lines", "b-ed-lines-in-scope"))
	for i := 0; i < total-1; i++ {
		targets = append(targets, economics.AllocationTarget{
			TargetID: fmt.Sprintf("t-foreign-%04d", i),
			Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, account.ID, "a-ed-alloc-bounds", "call-ed-lines-foreign", fmt.Sprintf("b-ed-lines-foreign-%04d", i)),
			Weight:   economics.AllocationFraction{Numerator: "1", Denominator: fmt.Sprint(total)},
		})
	}
	// The single in-scope target keeps the same exact share as every foreign
	// target so the envelope conserves to exactly one.
	targets[0].Weight = economics.AllocationFraction{Numerator: "1", Denominator: fmt.Sprint(total)}
	record := edTestAllocationQuantityEnvelope(t, store, "alloc-ed-lines-over", account.ID, targets)
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-lines"}, []string{"b-ed-lines-in-scope"})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got)
}

// TestEconomicDetailAllocationLineBudgetBoundaryPasses proves exactly the
// cumulative line bound is accepted and the in-scope contribution survives,
// while the bound fails one target over (previous test).
func TestEconomicDetailAllocationLineBudgetBoundaryPasses(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-lines-max", "USD")

	total := billing.MaxEconomicDetailAllocations
	targets := make([]economics.AllocationTarget, 0, total)
	targets = append(targets, economics.AllocationTarget{
		TargetID: "t-in-scope",
		Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, account.ID, "a-ed-alloc-bounds", "call-ed-lines-max", "b-ed-lines-max"),
		Weight:   economics.AllocationFraction{Numerator: "1", Denominator: fmt.Sprint(total)},
	})
	for i := 0; i < total-1; i++ {
		targets = append(targets, economics.AllocationTarget{
			TargetID: fmt.Sprintf("t-foreign-%04d", i),
			Target:   edTestBLegSubject(store.StoreID(), edAllocTestTenant, account.ID, "a-ed-alloc-bounds", "call-ed-lines-max-foreign", fmt.Sprintf("b-ed-lines-max-foreign-%04d", i)),
			Weight:   economics.AllocationFraction{Numerator: "1", Denominator: fmt.Sprint(total)},
		})
	}
	record := edTestAllocationQuantityEnvelope(t, store, "alloc-ed-lines-max", account.ID, targets)
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-lines-max"}, []string{"b-ed-lines-max"})
	require.NoError(t, err)
	require.Len(t, got, 1, "only the authoritative in-scope contribution survives")
	require.Equal(t, "t-in-scope", got[0].TargetID)
}

// TestEconomicDetailAllocationDiscoveryDuplicatesDoNotConsumeBudget proves a
// shared envelope discovered in more than one target chunk is counted exactly
// once: the global distinct budget is not consumed by a cross-chunk duplicate
// and no later unique envelope is hidden by a per-chunk LIMIT.
func TestEconomicDetailAllocationDiscoveryDuplicatesDoNotConsumeBudget(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-dup", "USD")

	const sharedX = "call-ed-dup-x"
	const sharedY = "call-ed-dup-y"
	// Every envelope targets X in chunk one; the first also targets Y, which
	// lands in chunk two, so it is discovered twice.
	for i := 0; i < economicDetailMaxAllocationRecords; i++ {
		targets := []economics.AllocationTarget{edTestAllocationCallTarget(store, account.ID, sharedX)}
		if i == 0 {
			targets = append(targets, edTestAllocationCallTarget(store, account.ID, sharedY))
			targets[0].Weight = economics.AllocationFraction{Numerator: "1", Denominator: "2"}
			targets[1].Weight = economics.AllocationFraction{Numerator: "1", Denominator: "2"}
		}
		record := edTestAllocationQuantityEnvelope(t, store, fmt.Sprintf("alloc-ed-dup-%03d", i), account.ID, targets)
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	callIDs := []string{sharedX}
	for i := 0; i < economicDetailChunkSize-1; i++ {
		callIDs = append(callIDs, fmt.Sprintf("call-pad-dup-%04d", i))
	}

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), callIDs, []string{sharedY})
	require.NoError(t, err)
	require.Len(t, got, economicDetailMaxAllocationRecords,
		"the cross-chunk duplicate must not overflow the budget and no unique envelope may be hidden")
	require.Equal(t, sharedX, got[0].Target.BillingCallID)
}

// TestEconomicDetailAllocationEnvelopeLoadIsOneBatchQuery proves the exact
// envelope load is a bounded batch rather than a one-query-per-envelope
// fan-out: one indexed billing_allocations SELECT returns every candidate.
func TestEconomicDetailAllocationEnvelopeLoadIsOneBatchQuery(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-batch", "USD")
	const envelopes = 8
	for i := 0; i < envelopes; i++ {
		record := edTestAllocationQuantityEnvelope(t, store, fmt.Sprintf("alloc-ed-batch-%03d", i), account.ID,
			[]economics.AllocationTarget{edTestAllocationBLegTarget(store, account.ID, "call-ed-batch", "b-ed-batch")})
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)
	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), []string{"call-ed-batch"}, []string{"b-ed-batch"})
	require.NoError(t, err)
	require.Len(t, got, envelopes)

	loadQueries, discoveryQueries := 0, 0
	for _, query := range recorder.queries {
		if strings.Contains(query, "FROM billing_allocations") {
			loadQueries++
		}
		if strings.Contains(query, "billing_allocation_targets") {
			discoveryQueries++
		}
	}
	require.Equal(t, 1, loadQueries, "envelope load must be one bounded batch query; recorded: %v", recorder.queries)
	require.Equal(t, 1, discoveryQueries, "one discovery chunk for a single target ID; recorded: %v", recorder.queries)
}

// TestEconomicDetailAllocationDiscoveryBudgetIsGlobalAcrossChunks proves the
// envelope budget is one global budget rather than a per-chunk budget: two
// individually under-bound chunks plus one shared envelope still overflow.
func TestEconomicDetailAllocationDiscoveryBudgetIsGlobalAcrossChunks(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-alloc-chunk", "USD")

	const sharedX = "call-ed-chunk-x"
	const sharedY = "call-ed-chunk-y"
	for i := 0; i <= economicDetailMaxAllocationRecords; i++ {
		callID := sharedY
		if i < economicDetailMaxAllocationRecords-24 {
			callID = sharedX // 40 envelopes in chunk one
		}
		record := edTestAllocationQuantityEnvelope(t, store, fmt.Sprintf("alloc-ed-chunk-%03d", i), account.ID,
			[]economics.AllocationTarget{edTestAllocationCallTarget(store, account.ID, callID)})
		require.NoError(t, store.AppendAllocation(ctx, record))
	}

	callIDs := []string{sharedX}
	for i := 0; i < economicDetailChunkSize-1; i++ {
		callIDs = append(callIDs, fmt.Sprintf("call-pad-chunk-%04d", i))
	}

	got, err := store.detailAllocations(ctx, edTestAllocationQuery(store, account.ID), callIDs, []string{sharedY})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	require.Empty(t, got)
}
