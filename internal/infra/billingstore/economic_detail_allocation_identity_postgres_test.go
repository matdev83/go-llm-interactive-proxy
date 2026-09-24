//go:build integration

package billingstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// TestPhase16Finding1AllocationIdentityPostgresDirect proves the sixth-pass
// Finding 1 durable contract on configured PostgreSQL: an allocation-only
// valuation correction persists as a distinct revision, keeps the legacy
// observation-only identity when no allocation coverage is present, and the
// exact selected lookup resolves the intended allocation-aware revision.
func TestPhase16Finding1AllocationIdentityPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	subject, refs := edFinding1Scope(t, store, "f1-pg")
	allocV1 := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocV2 := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-f1", Version: 2, PayloadHash: strings.Repeat("b", 64)}

	initial := edFinding1Valuation(t, store, "val-f1-pg-initial", subject, refs, []economics.AllocationRef{allocV1}, "1.00")
	require.NoError(t, store.AppendValuation(ctx, initial))
	initialHash := edFinding1StoredInputHash(t, store, initial.ID)

	replacement := edFinding1Valuation(t, store, "val-f1-pg-replacement", subject, refs, []economics.AllocationRef{allocV2}, "1.25")
	require.NoError(t, store.AppendValuation(ctx, replacement),
		"an allocation-only correction must persist on PostgreSQL")
	replacementHash := edFinding1StoredInputHash(t, store, replacement.ID)
	require.NotEqual(t, initialHash, replacementHash)

	reloaded, err := store.GetValuation(ctx, replacement.ID, economics.ValuationVersionV2)
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{allocV2}, reloaded.AllocationCoverageRefs)

	require.NoError(t, store.AppendValuation(ctx, replacement), "exact replay must stay idempotent on PostgreSQL")

	loaded, err := store.detailSelectedValuations(ctx, "tenant-f1", []billing.SelectedCostValuationRef{{
		ValuationID: replacement.ID, Revision: 1, InputSetHash: replacementHash,
	}})
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, replacement.ID, loaded[0].ID)
	require.Equal(t, []economics.AllocationRef{allocV2}, loaded[0].AllocationCoverageRefs)

	// Legacy compatibility: no allocation coverage keeps the observation-only
	// identity byte-for-byte on PostgreSQL.
	plain := edTestValuation(t, "val-f1-pg-plain", economics.BasisProviderReported, subject, refs, edTestCurrencyTotal(t, "USD", "1.00"))
	require.NoError(t, store.AppendValuation(ctx, plain))
	legacyHash, err := economics.CanonicalInputSetHash(plain.Basis, plain.InputObservations)
	require.NoError(t, err)
	require.Equal(t, legacyHash, edFinding1StoredInputHash(t, store, plain.ID))
}

// TestPhase16SeventhPassAllocationReplacementPostgresDirect proves the
// seventh-pass residual closure on configured PostgreSQL: an allocation-only
// replacement with unchanged observations persists as a distinct immutable
// revision through the composed worker and advances the selected head, while
// the observation fence is shared.
func TestPhase16SeventhPassAllocationReplacementPostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx := context.Background()
	bunDB, _ := openIsolatedPostgresBun(t, dsn, 4)
	store, err := NewDurableStore(ctx, bunDB, Config{StoreID: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, VerifySchema(ctx, store.db))

	observation := phase4EconomicsObservation("test", "seventhpass-replacement-pg", 55)
	allocV1, allocV2 := seventhPassReplacementAllocations(store.StoreID())
	headKey := "seventhpass-replacement-pg-head"
	first := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_240_000, 0).UTC())
	second := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV2}, headKey, time.Unix(1_700_240_100, 0).UTC())
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))

	rater := &seventhPassEchoAllocationRater{}
	worker, err := billing.NewEconomicRevisionWorker(store, store, rater, billing.EconomicQueueProvider, 8)
	require.NoError(t, err)
	require.NoError(t, worker.ProcessOnce(ctx))
	require.Equal(t, 2, rater.calls)
	seventhPassAssertReplacementState(t, store, first, second, allocV1, allocV2)
}
