package billingstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 sixth-pass Finding 1 durable boundary proof: a valuation corrected
// solely by changing which immutable allocation revisions it proves included
// must persist as a distinct, reloadable revision even when its observation
// inputs, subject, rater, policy and tariff are unchanged. Reordering the same
// allocation set is the same identity; empty allocation coverage keeps the
// legacy observation-only identity.

func edFinding1Ref(storeID, observationID string) metering.ObservationRef {
	return metering.ObservationRef{
		StoreID: storeID, ObservationID: observationID, Revision: 1, PayloadHash: strings.Repeat("1", 64),
	}
}

// edFinding1Valuation builds a complete provider-reported valuation whose input
// identity is left empty so the trusted durable boundary derives it from the
// observation and allocation coverage sets under test.
func edFinding1Valuation(t *testing.T, store *DurableStore, id string, subject metering.SubjectRef, refs []metering.ObservationRef, allocations []economics.AllocationRef, amount string) economics.Valuation {
	t.Helper()
	valuation := edTestValuation(t, id, economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", amount))
	valuation.AllocationCoverageRefs = append([]economics.AllocationRef(nil), allocations...)
	valuation.InputSetHash = ""
	require.NoError(t, valuation.Validate())
	return valuation
}

func edFinding1StoredInputHash(t *testing.T, store *DurableStore, valuationID string) string {
	t.Helper()
	var hash string
	require.NoError(t, store.db.NewRaw(
		`SELECT input_set_hash FROM billing_valuations WHERE store_id = ? AND valuation_id = ? AND valuation_version = ?`,
		store.StoreID(), valuationID, int64(economics.ValuationVersionV2)).Scan(context.Background(), &hash))
	return hash
}

func edFinding1Scope(t *testing.T, store *DurableStore, prefix string) (metering.SubjectRef, []metering.ObservationRef) {
	t.Helper()
	callID := edTestCallID(t)
	subject := edTestBLegSubject(store.StoreID(), "tenant-f1", "acct-f1", "a-f1", callID.String(), "b-"+prefix)
	refs := []metering.ObservationRef{
		edFinding1Ref(store.StoreID(), "obs-"+prefix+"-1"),
		edFinding1Ref(store.StoreID(), "obs-"+prefix+"-2"),
	}
	return subject, refs
}

func TestPhase16Finding1AllocationOnlyRevisionPersists(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	subject, refs := edFinding1Scope(t, store, "f1-revision")

	allocV1 := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocV2 := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 2, PayloadHash: strings.Repeat("b", 64)}

	initial := edFinding1Valuation(t, store, "val-f1-initial", subject, refs, []economics.AllocationRef{allocV1}, "1.00")
	require.NoError(t, store.AppendValuation(ctx, initial))
	initialHash := edFinding1StoredInputHash(t, store, initial.ID)

	replacement := edFinding1Valuation(t, store, "val-f1-replacement", subject, refs, []economics.AllocationRef{allocV2}, "1.25")
	require.NoError(t, store.AppendValuation(ctx, replacement),
		"an allocation-only correction with unchanged observation inputs must persist as a new revision")
	replacementHash := edFinding1StoredInputHash(t, store, replacement.ID)
	require.NotEqual(t, initialHash, replacementHash,
		"changing allocation coverage must change the durable input identity")

	reloaded, err := store.GetValuation(ctx, replacement.ID, economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{allocV2}, reloaded.AllocationCoverageRefs)
	require.Equal(t, replacementHash, reloaded.InputSetHash,
		"the reloaded revision must retain its allocation-aware input identity")

	require.NoError(t, store.AppendValuation(ctx, replacement), "exact replay of the corrected revision must stay idempotent")
}

func TestPhase16Finding1AllocationIdentityLegacyOrderingAndGuards(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	subject, refs := edFinding1Scope(t, store, "f1-legacy")

	// Legacy compatibility: no allocation coverage keeps the observation-only
	// input identity byte-for-byte.
	plain := edTestValuation(t, "val-f1-plain", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.00"))
	require.NoError(t, store.AppendValuation(ctx, plain))
	legacyHash, err := economics.CanonicalInputSetHash(plain.Basis, plain.InputObservations)
	require.NoError(t, err)
	require.Equal(t, legacyHash, edFinding1StoredInputHash(t, store, plain.ID))

	allocA := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1-a", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocB := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1-b", Version: 1, PayloadHash: strings.Repeat("b", 64)}

	ordered := edFinding1Valuation(t, store, "val-f1-ordered", subject, refs, []economics.AllocationRef{allocA, allocB}, "1.00")
	require.NoError(t, store.AppendValuation(ctx, ordered))
	reorderedSameIdentity := ordered.Clone()
	reorderedSameIdentity.AllocationCoverageRefs = []economics.AllocationRef{allocB, allocA}
	require.NoError(t, store.AppendValuation(ctx, reorderedSameIdentity),
		"reordered allocation refs under the same revision identity must replay idempotently")
	reordered := edFinding1Valuation(t, store, "val-f1-reordered", subject, refs, []economics.AllocationRef{allocB, allocA}, "1.00")
	require.ErrorIs(t, store.AppendValuation(ctx, reordered), ErrIdentityConflict,
		"a reordered allocation set is the same canonical identity, not a new revision")

	// Duplicate allocation coverage refs are rejected before persistence.
	duplicate := edTestValuation(t, "val-f1-duplicate", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.00"))
	duplicate.AllocationCoverageRefs = []economics.AllocationRef{allocA, allocA}
	require.Error(t, duplicate.Validate())
	require.Error(t, store.AppendValuation(ctx, duplicate))

	// Structurally invalid allocation coverage refs are rejected before
	// persistence.
	invalid := edTestValuation(t, "val-f1-invalid", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.00"))
	invalid.AllocationCoverageRefs = []economics.AllocationRef{{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 0, PayloadHash: strings.Repeat("a", 64)}}
	require.Error(t, invalid.Validate())
	require.Error(t, store.AppendValuation(ctx, invalid))
}

func TestPhase16Finding1SelectedLookupResolvesAllocationAwareRevision(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	subject, refs := edFinding1Scope(t, store, "f1-lookup")

	allocA := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocB := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 2, PayloadHash: strings.Repeat("b", 64)}

	first := edFinding1Valuation(t, store, "val-f1-first", subject, refs, []economics.AllocationRef{allocA}, "1.00")
	require.NoError(t, store.AppendValuation(ctx, first))
	firstHash := edFinding1StoredInputHash(t, store, first.ID)

	replacement := edFinding1Valuation(t, store, "val-f1-second", subject, refs, []economics.AllocationRef{allocB}, "1.25")
	require.NoError(t, store.AppendValuation(ctx, replacement))
	replacementHash := edFinding1StoredInputHash(t, store, replacement.ID)

	loaded, err := store.detailSelectedValuations(ctx, "tenant-f1", []billing.SelectedCostValuationRef{{
		ValuationID: replacement.ID, Revision: 1, InputSetHash: replacementHash,
	}})
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, replacement.ID, loaded[0].ID)
	require.Equal(t, []economics.AllocationRef{allocB}, loaded[0].AllocationCoverageRefs,
		"the exact allocation-aware revision must resolve, not the older stream revision")

	loadedFirst, err := store.detailSelectedValuations(ctx, "tenant-f1", []billing.SelectedCostValuationRef{{
		ValuationID: first.ID, Revision: 1, InputSetHash: firstHash,
	}})
	require.NoError(t, err)
	require.Len(t, loadedFirst, 1)
	require.Equal(t, first.ID, loadedFirst[0].ID)
	require.Equal(t, []economics.AllocationRef{allocA}, loadedFirst[0].AllocationCoverageRefs)

	stale, err := store.detailSelectedValuations(ctx, "tenant-f1", []billing.SelectedCostValuationRef{{
		ValuationID: replacement.ID, Revision: 1, InputSetHash: firstHash,
	}})
	require.NoError(t, err)
	require.Empty(t, stale, "a mismatched input identity must fail closed")
}

// TestPhase16SeventhPassAppendEconomicRevisionResultDualFence pins the durable
// result writer's dual fence: the observation-only projection derived from the
// valuation's retained refs must equal the immutable work envelope hash, and
// the full allocation-aware hash must remain the valuation's canonical
// identity. Reordered identical allocation refs replay idempotently. This case
// intentionally exercises the legacy seam where the immutable work declares no
// allocation coverage: such a work has one observation-derived identity, so a
// replacement allocation under it is a conflicting immutable replay and must
// not overwrite the first result. Declared-allocation work (the new repair
// path) instead derives a distinct identity and persists the replacement; see
// TestPhase16SeventhPass{Worker,JobRunner}AllocationOnlyReplacementAdvances.
func TestPhase16SeventhPassAppendEconomicRevisionResultDualFence(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	work := economicJobRunnerStoreWork(t, billing.EconomicQueueProvider, 44, "seventhpass-dual-fence")
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)

	allocA := seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-a", 1, "a")
	allocB := seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-b", 1, "b")
	allocReplacement := seventhPassAllocationRef(store.StoreID(), "alloc-seventhpass-a", 2, "c")

	build := func(allocations []economics.AllocationRef, observationRefs []metering.ObservationRef) economics.Valuation {
		t.Helper()
		fullHash, hashErr := economics.CanonicalValuationInputSetHash(normalized.Input.Basis, observationRefs, allocations)
		require.NoError(t, hashErr)
		valuation := edTestValuation(t, identity.ValuationKey(), normalized.Input.Basis, normalized.Subject, observationRefs, edTestCurrencyTotal(t, "USD", "1.00"))
		valuation.AllocationCoverageRefs = append([]economics.AllocationRef(nil), allocations...)
		valuation.InputSetHash = fullHash
		require.NoError(t, valuation.Validate())
		return valuation
	}

	initial := build([]economics.AllocationRef{allocA, allocB}, normalized.Input.ObservationRefs)
	require.NoError(t, store.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: initial}))

	reloaded, err := store.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{allocA, allocB}, reloaded.AllocationCoverageRefs)

	observationHash, err := economics.CanonicalInputSetHash(normalized.Input.Basis, reloaded.InputObservations)
	require.NoError(t, err)
	require.Equal(t, normalized.InputSetHash, observationHash, "the work-envelope fence stays observation-only")
	require.NotEqual(t, normalized.InputSetHash, reloaded.InputSetHash, "the full valuation identity includes allocation coverage")
	fullHash, err := economics.CanonicalValuationInputSetHash(normalized.Input.Basis, reloaded.InputObservations, reloaded.AllocationCoverageRefs)
	require.NoError(t, err)
	require.Equal(t, fullHash, reloaded.InputSetHash)

	reordered := build([]economics.AllocationRef{allocB, allocA}, normalized.Input.ObservationRefs)
	require.Equal(t, reloaded.InputSetHash, reordered.InputSetHash, "reordered allocation refs must canonicalize to one identity")
	require.NoError(t, store.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: reordered}),
		"reordered identical allocation refs must replay idempotently")

	replacement := build([]economics.AllocationRef{allocA, allocReplacement}, normalized.Input.ObservationRefs)
	require.NotEqual(t, reloaded.InputSetHash, replacement.InputSetHash, "a replacement allocation revision is a distinct full identity")
	require.ErrorIs(t, store.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: replacement}),
		billing.ErrEconomicRevisionConflict, "a distinct allocation revision must not overwrite the immutable first result")

	driftedRefs := append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...)
	driftedRefs[0].ObservationID += "-seventhpass-drift"
	drifted := build([]economics.AllocationRef{allocA, allocB}, driftedRefs)
	require.ErrorIs(t, store.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: drifted}),
		billing.ErrEconomicRevisionInputMismatch, "a drifted observation set must fail the work fence")

	stale := build([]economics.AllocationRef{allocA, allocB}, normalized.Input.ObservationRefs)
	stale.InputSetHash = strings.Repeat("9", 64)
	require.ErrorIs(t, store.AppendEconomicRevisionResult(ctx, normalized, billing.EconomicRevisionResult{Valuation: stale}),
		economics.ErrInputSetHashMismatch, "a stale full allocation-aware hash must be rejected")
}
