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

// Finding 3 RED contract: the allocation correction state (status, pending
// ancestry, completeness, payable, safe audit references) must survive the
// public QueryEconomicDetail path, and authoritative supersession must be
// resolved over the whole bounded lineage so that a target-moving replacement
// can never leave a superseded contribution looking live. Unauthorized
// (account-less) source aggregates and remainders must be redacted rather than
// leaked by subtraction.

const edSupTenant = "tenant-ed"

func edSupAllocationSource(store *DurableStore, accountID, resourceID string) metering.SubjectRef {
	return metering.SubjectRef{
		Kind: metering.SubjectResource, StoreID: store.StoreID(), TenantID: edSupTenant,
		AccountID: accountID, ResourceID: resourceID, PeriodID: "2026-09",
		StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC(),
	}
}

func edSupAllocationPolicy() economics.AllocationPolicyRef {
	return economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)}
}

func edBalanceAllocationRef(store *DurableStore, record economics.AllocationRecord) economics.AllocationRef {
	canonical, err := record.Canonical()
	if err != nil {
		panic(err)
	}
	return economics.AllocationRef{
		StoreID: store.StoreID(), AllocationID: canonical.ID,
		Version: canonical.Version, PayloadHash: canonical.Fingerprint(),
	}
}

func edAllocationRefIDs(refs []economics.AllocationRef) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.AllocationID)
	}
	return out
}

// TestQueryEconomicDetailAllocationTargetMovingReplacementIsNotLive is Finding
// 3(a): v1 targets call A and a replacement v2 reallocates the same conserved
// source entirely to call B. Querying A must not report v1 as a live scoped
// contribution; it must expose truthful supersession state instead.
func TestQueryEconomicDetailAllocationTargetMovingReplacementIsNotLive(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-sup-move", "USD")
	callA := edTestCallID(t)
	callB := edTestCallID(t)
	edSetupCall(t, store, account.ID, callA, "a-ed-sup-move", edTestLeg(t, "b-ed-sup-a"))
	edSetupCall(t, store, account.ID, callB, "a-ed-sup-move-b", edTestLeg(t, "b-ed-sup-b"))

	amount := edTestDecimal(t, "10")
	v1 := economics.AllocationRecord{
		ID: "alloc-sup-move-v1", Version: 1,
		SourceSubject: edSupAllocationSource(store, account.ID, "shared-move"),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-a", Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-move", callA.String(), "b-ed-sup-a"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, v1))

	v2 := v1.Clone()
	v2.ID = "alloc-sup-move-v2"
	v2.Operation = economics.AllocationOperationReplacement
	v2.Supersedes = []economics.AllocationRef{edBalanceAllocationRef(store, v1)}
	v2.Targets = []economics.AllocationTarget{
		{TargetID: "t-b", Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-move-b", callB.String(), "b-ed-sup-b"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
	}
	require.NoError(t, store.AppendAllocation(ctx, v2))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callA.String(), ALegID: "a-ed-sup-move",
	})
	require.NoError(t, err)
	require.Empty(t, got.Coverage.Allocations, "a superseded contribution must never be reported as a live scoped allocation")
	require.NotNil(t, got.Coverage.AllocationState, "supersession state must survive the public reader")
	require.Equal(t, economics.AllocationSupersessionResolved, got.Coverage.AllocationState.Status)
	require.True(t, got.Coverage.AllocationState.Complete)
	require.True(t, got.Coverage.AllocationState.Payable)
	require.Contains(t, edAllocationRefIDs(got.Coverage.AllocationState.Superseded), v1.ID,
		"the superseded predecessor must remain auditable")
}

// TestQueryEconomicDetailAllocationMissingPredecessorIsPending is Finding 3(b):
// a discovered successor names a predecessor that was never persisted. The
// reader must fail closed with explicit pending/incomplete state instead of
// silently presenting an empty or complete contribution set.
func TestQueryEconomicDetailAllocationMissingPredecessorIsPending(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-sup-missing", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-sup-missing", edTestLeg(t, "b-ed-sup-missing"))

	amount := edTestDecimal(t, "10")
	v2 := economics.AllocationRecord{
		ID: "alloc-sup-missing-v2", Version: 1,
		SourceSubject: edSupAllocationSource(store, account.ID, "shared-missing"),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationReplacement,
		Supersedes: []economics.AllocationRef{
			{StoreID: store.StoreID(), AllocationID: "alloc-sup-missing-v1", Version: 1, PayloadHash: strings.Repeat("f", 64)},
		},
		RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven,
		RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-a", Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-missing", callID.String(), "b-ed-sup-missing"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, v2))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-sup-missing",
	})
	require.NoError(t, err)
	require.Empty(t, got.Coverage.Allocations, "a successor with a missing predecessor is not an effective contribution")
	require.NotNil(t, got.Coverage.AllocationState, "pending ancestry must not look like an absent allocation set")
	require.Equal(t, economics.AllocationSupersessionPending, got.Coverage.AllocationState.Status)
	require.False(t, got.Coverage.AllocationState.Complete)
	require.False(t, got.Coverage.AllocationState.Payable)
	require.Contains(t, edAllocationRefIDs(got.Coverage.AllocationState.Pending), "alloc-sup-missing-v1")
}

// TestQueryEconomicDetailAllocationPendingAncestryKeepsPartialLines is Finding
// 3(c): a resolved contribution and a pending correction share one scope. The
// returned lines are only partial, so the pending/completeness/audit state must
// survive alongside them instead of being discarded with the line slice.
func TestQueryEconomicDetailAllocationPendingAncestryKeepsPartialLines(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-sup-partial", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-sup-partial", edTestLeg(t, "b-ed-sup-partial"))

	amount := edTestDecimal(t, "10")
	base := economics.AllocationRecord{
		ID: "alloc-sup-partial-base", Version: 1,
		SourceSubject: edSupAllocationSource(store, account.ID, "shared-partial"),
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-base", Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-partial", callID.String(), "b-ed-sup-partial"), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, store.AppendAllocation(ctx, base))

	pending := base.Clone()
	pending.ID = "alloc-sup-partial-pending"
	pending.Operation = economics.AllocationOperationCorrection
	pending.Supersedes = []economics.AllocationRef{
		{StoreID: store.StoreID(), AllocationID: "alloc-sup-partial-lateparent", Version: 1, PayloadHash: strings.Repeat("e", 64)},
	}
	require.NoError(t, store.AppendAllocation(ctx, pending))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-sup-partial",
	})
	require.NoError(t, err)
	require.Len(t, got.Coverage.Allocations, 1, "the resolved base contribution remains partial evidence")
	require.Equal(t, base.ID, got.Coverage.Allocations[0].AllocationID)
	require.NotNil(t, got.Coverage.AllocationState)
	require.Equal(t, economics.AllocationSupersessionPending, got.Coverage.AllocationState.Status)
	require.False(t, got.Coverage.AllocationState.Complete)
	require.Contains(t, edAllocationRefIDs(got.Coverage.AllocationState.Pending), "alloc-sup-partial-lateparent")
}

// TestQueryEconomicDetailAllocationClosureBoundFailsClosed proves the
// supersession closure is bounded before Go growth: one in-scope seed whose
// source subject carries an over-bound unrelated lineage fails closed at the
// database boundary instead of materializing the excess, and the closure query
// itself carries a database-side LIMIT.
func TestQueryEconomicDetailAllocationClosureBoundFailsClosed(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-sup-bound", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-sup-bound", edTestLeg(t, "b-ed-sup-bound"))

	amount := edTestDecimal(t, "10")
	makeRecord := func(id, targetID, bLegID string) economics.AllocationRecord {
		return economics.AllocationRecord{
			ID: id, Version: 1,
			SourceSubject: edSupAllocationSource(store, account.ID, "shared-bound"),
			SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
			Policy:    edSupAllocationPolicy(),
			Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
			RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToLastTarget,
			Targets: []economics.AllocationTarget{
				{TargetID: targetID, Target: edTestBLegSubject(store.StoreID(), edSupTenant, account.ID, "a-ed-sup-bound", callID.String(), bLegID), Weight: economics.AllocationFraction{Numerator: "1", Denominator: "1"}},
			},
			CreatedAt: time.Unix(300, 0).UTC(),
		}
	}
	// The only discovered seed targets the requested leg.
	require.NoError(t, store.AppendAllocation(ctx, makeRecord("alloc-sup-bound-seed", "t-seed", "b-ed-sup-bound")))
	// Unrelated same-source lineage that no requested target names.
	for i := range economicDetailMaxAllocationRecords {
		require.NoError(t, store.AppendAllocation(ctx, makeRecord(
			fmt.Sprintf("alloc-sup-bound-extra-%03d", i),
			fmt.Sprintf("t-extra-%03d", i), fmt.Sprintf("b-ed-sup-bound-extra-%03d", i),
		)))
	}

	recorder := &edLegQueryRecorder{}
	store.db.AddQueryHook(recorder)
	_, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-sup-bound",
	})
	require.ErrorIs(t, err, billing.ErrEconomicDetailBoundExceeded)
	bounded := false
	for _, query := range recorder.queries {
		if strings.Contains(query, "billing_allocations") && strings.Contains(query, "LIMIT") {
			bounded = true
		}
	}
	require.True(t, bounded, "the allocation closure query must carry a database-side LIMIT; recorded %v", recorder.queries)
}

// TestQueryEconomicDetailAccountScopedSharedEnvelopeRedactsAggregate is Finding
// 3(d): an account-less shared source envelope cannot prove the aggregate
// economics belong to the requested account. The scoped reader must keep the
// authoritative membership identity but redact the full source aggregate and
// remainder instead of leaking them by subtraction.
func TestQueryEconomicDetailAccountScopedSharedEnvelopeRedactsAggregate(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	account := edTestAccount(t, store, "ed-sup-redact", "USD")
	callID := edTestCallID(t)
	edSetupCall(t, store, account.ID, callID, "a-ed-sup-redact", edTestLeg(t, "b-ed-sup-redact"))

	amount := edTestDecimal(t, "10")
	record := economics.AllocationRecord{
		ID: "alloc-sup-redact", Version: 1,
		SourceSubject: metering.SubjectRef{
			Kind: metering.SubjectResource, StoreID: store.StoreID(),
			ResourceID: "shared-accountless", PeriodID: "2026-09",
		},
		SourceBasis: economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    edSupAllocationPolicy(),
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine,
		RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{TargetID: "t-accountless", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store.StoreID(), BLegID: "b-ed-sup-redact"}, Weight: economics.AllocationFraction{Numerator: "1", Denominator: "4"}},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "3", Denominator: "4"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
	require.NoError(t, record.Validate())
	require.NoError(t, store.AppendAllocation(ctx, record))

	got, err := store.QueryEconomicDetail(ctx, billing.EconomicDetailQuery{
		StoreID: store.StoreID(), AccountID: account.ID, BillingCallID: callID.String(), ALegID: "a-ed-sup-redact",
	})
	require.NoError(t, err)
	require.NotEmpty(t, got.Coverage.Allocations, "the authoritative in-scope membership must still be reported")
	require.NotNil(t, got.Coverage.AllocationState)
	require.True(t, got.Coverage.AllocationState.Redacted)
	for _, line := range got.Coverage.Allocations {
		require.True(t, line.Redacted, "unauthorized source economics must be explicitly redacted")
		require.Nil(t, line.SourceAmount, "the full source aggregate must never leak to an account scope")
		require.Nil(t, line.SourceQuantity, "the full source quantity must never leak to an account scope")
		require.Nil(t, line.RoundedAmount, "a per-target or remainder amount that reconstructs the aggregate must not leak")
	}
}
