package billingstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestPhase11AllocationRoundTripReplayConflictAndImmutableVersion(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	record := phase11AllocationRecord(t, "allocation-1", 1)
	want, err := record.CanonicalJSON()
	require.NoError(t, err)
	require.NoError(t, store.AppendAllocation(ctx, record))
	require.NoError(t, store.AppendAllocation(ctx, record))
	got, err := store.GetAllocation(ctx, record.ID, record.Version)
	require.NoError(t, err)
	gotJSON, err := got.CanonicalJSON()
	require.NoError(t, err)
	require.Equal(t, string(want), string(gotJSON))

	changed := record
	changed.Policy.Version = "v2"
	changed.Policy.Hash = strings.Repeat("b", 64)
	require.ErrorIs(t, store.AppendAllocation(ctx, changed), ErrIdentityConflict)

	revision := record
	revision.Operation = economics.AllocationOperationCorrection
	revision.Version = 2
	revision.Supersedes = []economics.AllocationRef{{StoreID: "test", AllocationID: record.ID, Version: record.Version, PayloadHash: record.Fingerprint()}}
	require.NoError(t, store.AppendAllocation(ctx, revision))
	page, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", SourceSubject: &record.SourceSubject, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Allocations, 2)
	require.Equal(t, record.ID, page.Allocations[0].ID)
	require.Equal(t, revision.ID, page.Allocations[1].ID)
	require.Equal(t, uint64(2), page.Allocations[1].Version)
	firstPage, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", SourceSubject: &record.SourceSubject, Limit: 1})
	require.NoError(t, err)
	require.Len(t, firstPage.Allocations, 1)
	require.NotEmpty(t, firstPage.NextCursor)
	secondPage, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", SourceSubject: &record.SourceSubject, Limit: 1, Cursor: firstPage.NextCursor})
	require.NoError(t, err)
	require.Len(t, secondPage.Allocations, 1)
	require.Equal(t, uint64(2), secondPage.Allocations[0].Version)

	var count int
	require.NoError(t, store.db.NewRaw(`SELECT COUNT(1) FROM billing_allocations`).Scan(ctx, &count))
	require.Equal(t, 2, count)
	require.NoError(t, VerifySchema(ctx, store.db))
}

func TestPhase11AllocationRejectsOutOfScopeAndMissingRecord(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	record := phase11AllocationRecord(t, "allocation-scope", 1)
	record.SourceSubject.StoreID = "other-store"
	require.ErrorIs(t, store.AppendAllocation(ctx, record), ErrEconomicsOutOfScope)
	_, err := store.GetAllocation(ctx, "missing", 1)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrIdentityConflict))
}

func TestPhase11AllocationSupersessionMissingThenArrivalConverges(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	base := phase11AllocationRecord(t, "allocation-late-base", 1)
	canonicalBase, err := base.Canonical()
	require.NoError(t, err)
	correction := canonicalBase.Clone()
	correction.ID = "allocation-late-replacement"
	correction.Operation = economics.AllocationOperationReplacement
	correction.Supersedes = []economics.AllocationRef{{StoreID: "test", AllocationID: canonicalBase.ID, Version: 1, PayloadHash: canonicalBase.Fingerprint()}}

	// Arrival order is intentionally reversed. The immutable successor is
	// accepted as explicitly pending and resolves when the predecessor arrives.
	require.NoError(t, store.AppendAllocation(ctx, correction))
	page, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", SourceSubject: &base.SourceSubject, Limit: 10})
	require.NoError(t, err)
	pending, err := economics.ResolveAllocationSupersession(page.Allocations)
	require.NoError(t, err)
	require.Equal(t, economics.AllocationSupersessionPending, pending.Status)
	require.Len(t, pending.Effective, 0)
	require.Len(t, pending.Pending, 1)

	require.NoError(t, store.AppendAllocation(ctx, base))
	require.NoError(t, store.AppendAllocation(ctx, correction))
	page, err = store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", SourceSubject: &base.SourceSubject, Limit: 10})
	require.NoError(t, err)
	resolved, err := economics.ResolveAllocationSupersession(page.Allocations)
	require.NoError(t, err)
	require.Equal(t, economics.AllocationSupersessionResolved, resolved.Status)
	require.True(t, resolved.Complete)
	require.True(t, resolved.Payable)
	require.Len(t, resolved.Effective, 1)
	require.Equal(t, correction.IdentityKey(), resolved.Effective[0].IdentityKey())
}

func TestPhase11AllocationSupersessionRejectsScopeHashForkAndCycle(t *testing.T) {
	ctx := context.Background()

	t.Run("scope and hash", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		base := phase11AllocationRecord(t, "allocation-append-base", 1)
		require.NoError(t, store.AppendAllocation(ctx, base))

		scope := base.Clone()
		scope.ID = "allocation-cross-scope"
		scope.Operation = economics.AllocationOperationCorrection
		scope.SourceSubject.ResourceID = "foreign-resource"
		scope.Supersedes = []economics.AllocationRef{{StoreID: "test", AllocationID: base.ID, Version: 1, PayloadHash: base.Fingerprint()}}
		require.ErrorIs(t, store.AppendAllocation(ctx, scope), economics.ErrAllocationScopeMismatch)

		hash := base.Clone()
		hash.ID = "allocation-bad-hash"
		hash.Operation = economics.AllocationOperationCorrection
		hash.Supersedes = []economics.AllocationRef{{StoreID: "test", AllocationID: base.ID, Version: 1, PayloadHash: strings.Repeat("e", 64)}}
		require.ErrorIs(t, store.AppendAllocation(ctx, hash), economics.ErrAllocationSupersessionConflict)
	})

	t.Run("fork", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		base := phase11AllocationRecord(t, "allocation-fork-base", 1)
		canonicalBase, err := base.Canonical()
		require.NoError(t, err)
		require.NoError(t, store.AppendAllocation(ctx, canonicalBase))
		ref := economics.AllocationRef{StoreID: "test", AllocationID: canonicalBase.ID, Version: 1, PayloadHash: canonicalBase.Fingerprint()}

		left := canonicalBase.Clone()
		left.ID = "allocation-fork-left"
		left.Operation = economics.AllocationOperationCorrection
		left.Supersedes = []economics.AllocationRef{ref}
		right := canonicalBase.Clone()
		right.ID = "allocation-fork-right"
		right.Operation = economics.AllocationOperationCorrection
		right.Supersedes = []economics.AllocationRef{ref}
		require.NoError(t, store.AppendAllocation(ctx, left))
		require.ErrorIs(t, store.AppendAllocation(ctx, right), economics.ErrAllocationSupersessionFork)
	})

	t.Run("cycle across durable appends", func(t *testing.T) {
		store := newSQLiteTestStore(t)
		left := phase11AllocationRecord(t, "allocation-cycle-left", 1)
		left.Operation = economics.AllocationOperationReplacement
		left.Supersedes = []economics.AllocationRef{{StoreID: "test", AllocationID: "allocation-cycle-right", Version: 1, PayloadHash: strings.Repeat("f", 64)}}
		right := phase11AllocationRecord(t, "allocation-cycle-right", 1)
		right.Operation = economics.AllocationOperationReplacement
		right.Supersedes = []economics.AllocationRef{{StoreID: "test", AllocationID: "allocation-cycle-left", Version: 1, PayloadHash: strings.Repeat("f", 64)}}
		require.NoError(t, store.AppendAllocation(ctx, left))
		require.ErrorIs(t, store.AppendAllocation(ctx, right), economics.ErrAllocationSupersessionCycle)
	})
}

func TestPhase11AllocationConcurrentSuccessorsHaveOneDurableWinner(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	base := phase11AllocationRecord(t, "allocation-concurrent-base", 1)
	require.NoError(t, store.AppendAllocation(ctx, base))
	canonicalBase, err := base.Canonical()
	require.NoError(t, err)
	ref := economics.AllocationRef{StoreID: "test", AllocationID: canonicalBase.ID, Version: canonicalBase.Version, PayloadHash: canonicalBase.Fingerprint()}

	left := canonicalBase.Clone()
	left.ID = "allocation-concurrent-left"
	left.Operation = economics.AllocationOperationCorrection
	left.Supersedes = []economics.AllocationRef{ref}
	right := canonicalBase.Clone()
	right.ID = "allocation-concurrent-right"
	right.Operation = economics.AllocationOperationCorrection
	right.Supersedes = []economics.AllocationRef{ref}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, record := range []economics.AllocationRecord{left, right} {
		wg.Go(func() {
			<-start
			errs <- store.AppendAllocation(ctx, record)
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	forks := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, economics.ErrAllocationSupersessionFork):
			forks++
		default:
			t.Fatalf("concurrent successor error = %v, want one fork rejection", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, forks)

	page, err := store.ListAllocations(ctx, economics.AllocationQuery{StoreID: "test", SourceSubject: &base.SourceSubject, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Allocations, 2)
	resolved, err := economics.ResolveAllocationSupersession(page.Allocations)
	require.NoError(t, err)
	require.Equal(t, economics.AllocationSupersessionResolved, resolved.Status)
	require.True(t, resolved.Complete)
	require.True(t, resolved.Payable)
	require.Len(t, resolved.Effective, 1)
}

func phase11AllocationRecord(t *testing.T, id string, version uint64) economics.AllocationRecord {
	t.Helper()
	amount, err := metering.ParseDecimal("10")
	require.NoError(t, err)
	return economics.AllocationRecord{
		ID: id, Version: version,
		SourceSubject: metering.SubjectRef{Kind: metering.SubjectResource, StoreID: "test", TenantID: "tenant", AccountID: "supplier", ResourceID: "shared", PeriodID: "2026-09", StartAt: time.Unix(100, 0).UTC(), EndAt: time.Unix(200, 0).UTC()},
		SourceBasis:   economics.BasisAllocatedCost, SourceAmount: &amount, Currency: "USD",
		Policy:    economics.AllocationPolicyRef{Method: "weighted", Version: "v1", Hash: strings.Repeat("a", 64)},
		Operation: economics.AllocationOperationAllocate, RoundingScope: economics.RoundingScopeLine, RoundingPolicy: economics.RoundingHalfEven, RoundingResidualPolicy: economics.AllocationResidualToUnallocated,
		Targets: []economics.AllocationTarget{
			{TargetID: "b-leg-1", Target: metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "test", TenantID: "tenant", BLegID: "b-leg-1"}, Weight: economics.AllocationFraction{Numerator: "3", Denominator: "5"}},
			{TargetID: "unallocated", Unallocated: true, Weight: economics.AllocationFraction{Numerator: "2", Denominator: "5"}},
		},
		CreatedAt: time.Unix(300, 0).UTC(),
	}
}
