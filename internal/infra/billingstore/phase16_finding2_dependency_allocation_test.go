package billingstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 eighth-pass Finding 2 SQLite proofs: dependent reconciliation must
// resolve the exact allocation-aware producer output across reopen/retry and
// replacement. Dependencies carry DerivationHash; OutputIdentity reconstructs
// the producer's full revision identity; the dependency hash binds it.

func phase16Finding2SQLiteDependency(t *testing.T, work billing.EconomicRevisionWork) billing.EconomicJobDependency {
	t.Helper()
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	dependency, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	return dependency
}

func phase16Finding2SQLiteReconciliation(t *testing.T, subjectStoreID string, deps ...billing.EconomicJobDependency) billing.EconomicRevisionWork {
	t.Helper()
	observation := phase4EconomicsObservation(subjectStoreID, "finding2-reconciliation-evidence", 77)
	input := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "call", Observations: []metering.Observation{observation},
	}
	return billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, Kind: billing.EconomicWorkKindReconciliation,
		HeadKey: "finding2-reconciliation-head", Subject: observation.Subject,
		EvidenceRevision: 77, Input: input, Dependencies: deps,
		CreatedAt: time.Unix(1_700_310_500, 0).UTC(),
	}
}

func TestPhase16Finding2SQLiteAllocationRatingPromotesReconciliation(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "finding2-promote", 71)
	allocV1, _ := seventhPassReplacementAllocations(store.StoreID())
	headKey := "finding2-promote-head"
	rating := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_310_000, 0).UTC())
	dependency := phase16Finding2SQLiteDependency(t, rating)
	ratingID, err := rating.Identity()
	require.NoError(t, err)
	require.NotEmpty(t, ratingID.DerivationHash)
	require.Equal(t, ratingID.DerivationHash, dependency.DerivationHash)

	reconciliation := phase16Finding2SQLiteReconciliation(t, "test", dependency)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, rating))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))

	// Before the allocation-aware rating completes, the dependent is gated:
	// probe reports missing.
	checks, err := store.EconomicRevisionDependencyChecks(ctx, reconciliation)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	require.Equal(t, billing.EconomicJobDependencyMissing, checks[0].Status)
	backlog, err := store.EconomicRevisionQueueBacklog(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, backlog.IncompleteDependencies)

	rater := &seventhPassEchoAllocationRater{}
	reconciler := &economicJobRunnerStoreReconciler{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
	require.NoError(t, err)
	first, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, first.Completed)
	require.Equal(t, 1, rater.calls)

	// The exact allocation-aware valuation/revision is now durably present.
	valuation, err := store.LoadEconomicRevisionDependencyOutput(ctx, dependency)
	require.NoError(t, err)
	require.Equal(t, ratingID.ValuationKey(), valuation.ID)
	require.Equal(t, []economics.AllocationRef{allocV1}, valuation.AllocationCoverageRefs)
	checks, err = store.EconomicRevisionDependencyChecks(ctx, reconciliation)
	require.NoError(t, err)
	require.Equal(t, billing.EconomicJobDependencySatisfied, checks[0].Status)

	second, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, second.Completed)
	require.Len(t, reconciler.calls, 1)
	require.Equal(t, ratingID.ValuationKey(), reconciler.calls[0].Valuation.ID)
	require.Equal(t, dependency, reconciler.calls[0].Dependency)

	identity, err := reconciliation.Identity()
	require.NoError(t, err)
	probed, err := store.HasEconomicRevisionReconciliation(ctx, identity)
	require.NoError(t, err)
	require.True(t, probed)

	// Exact replay across reopen/retry is idempotent and never re-rates.
	third, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Zero(t, third.Claimed)
	require.Equal(t, 1, rater.calls)
	require.Len(t, reconciler.calls, 1)
}

func TestPhase16Finding2SQLiteReplacementFollowsNewHeadNeverOld(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "finding2-replacement", 72)
	allocV1, allocV2 := seventhPassReplacementAllocations(store.StoreID())
	headKey := "finding2-replacement-head"

	// Old-output presence: a legacy observation-only valuation over the same
	// observation plane persists first. It must never satisfy the
	// allocation-aware dependency.
	legacyInput := economics.PostUsageRatingInput{
		Version: 2, Perspective: metering.PerspectiveOperator, Basis: economics.BasisProviderReported,
		Subject: observation.Subject, Scope: "b_leg", Payer: metering.PaymentParty{Kind: metering.PaymentPartyOperator},
		Observations: []metering.Observation{observation.Clone()},
	}
	legacyWork := billing.EconomicRevisionWork{
		Queue: billing.EconomicQueueProvider, HeadKey: headKey, Subject: observation.Subject,
		EvidenceRevision: observation.Revision, Input: legacyInput, CreatedAt: time.Unix(1_700_310_900, 0).UTC(),
	}
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, legacyWork))
	require.NoError(t, store.AppendEconomicRevisionResult(ctx, legacyWork, billing.EconomicRevisionResult{Valuation: refinement82ValuationFor(t, legacyWork)}))

	first := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_311_000, 0).UTC())
	second := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV2}, headKey, time.Unix(1_700_311_100, 0).UTC())
	firstDep := phase16Finding2SQLiteDependency(t, first)
	secondDep := phase16Finding2SQLiteDependency(t, second)
	require.NotEqual(t, firstDep.Key(), secondDep.Key())
	firstID, err := first.Identity()
	require.NoError(t, err)
	secondID, err := second.Identity()
	require.NoError(t, err)
	require.NotEqual(t, firstID.ValuationKey(), secondID.ValuationKey())

	// A legacy observation-only dependency still binds the old output, never
	// the allocation-aware replacement.
	legacyDep := economicJobSQLiteDependency(t, legacyWork)
	legacyLoaded, err := store.LoadEconomicRevisionDependencyOutput(ctx, legacyDep)
	require.NoError(t, err)
	require.NotEqual(t, secondID.ValuationKey(), legacyLoaded.ID)

	reconciliation := phase16Finding2SQLiteReconciliation(t, "test", secondDep)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, first))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, second))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))

	rater := &seventhPassEchoAllocationRater{}
	reconciler := &economicJobRunnerStoreReconciler{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
	require.NoError(t, err)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Completed, "legacy, initial and replacement ratings complete; reconciliation stays gated")

	loaded, err := store.LoadEconomicRevisionDependencyOutput(ctx, secondDep)
	require.NoError(t, err)
	require.Equal(t, secondID.ValuationKey(), loaded.ID)
	require.Equal(t, []economics.AllocationRef{allocV2}, loaded.AllocationCoverageRefs)

	promoted, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, promoted.Completed)
	require.Len(t, reconciler.calls, 1)
	require.Equal(t, secondID.ValuationKey(), reconciler.calls[0].Valuation.ID)
	require.Equal(t, secondDep, reconciler.calls[0].Dependency)

	// The first allocation derivation remains independently queryable and is
	// never returned for the replacement dependency.
	firstLoaded, err := store.LoadEconomicRevisionDependencyOutput(ctx, firstDep)
	require.NoError(t, err)
	require.Equal(t, firstID.ValuationKey(), firstLoaded.ID)
	require.NotEqual(t, firstLoaded.ID, reconciler.calls[0].Valuation.ID)
}

func TestPhase16Finding2SQLiteReorderedRefsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "finding2-reorder", 73)
	allocA := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-reorder-a", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocB := economics.AllocationRef{StoreID: store.StoreID(), AllocationID: "alloc-reorder-b", Version: 1, PayloadHash: strings.Repeat("b", 64)}
	headKey := "finding2-reorder-head"
	ordered := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocA, allocB}, headKey, time.Unix(1_700_312_000, 0).UTC())
	reordered := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocB, allocA}, headKey, time.Unix(1_700_312_000, 0).UTC())
	orderedDep := phase16Finding2SQLiteDependency(t, ordered)
	reorderedDep := phase16Finding2SQLiteDependency(t, reordered)
	require.True(t, orderedDep.Equal(reorderedDep))
	require.Equal(t, orderedDep.Key(), reorderedDep.Key())

	require.NoError(t, store.AppendEconomicRevisionWork(ctx, ordered))
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reordered), "reordered identical allocation set is the same queue item")

	reconciliation := phase16Finding2SQLiteReconciliation(t, "test", orderedDep)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, reconciliation))

	rater := &seventhPassEchoAllocationRater{}
	reconciler := &economicJobRunnerStoreReconciler{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, reconciler))
	require.NoError(t, err)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Completed)
	require.Equal(t, 1, rater.calls)
	promoted, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, promoted.Completed)
	require.Equal(t, 1, rater.calls, "reordered identical set must not produce a second revision")
}

func TestPhase16Finding2SQLiteTamperedMissingRejected(t *testing.T) {
	t.Parallel()
	store := newSQLiteTestStore(t)
	ctx := context.Background()
	observation := phase4EconomicsObservation("test", "finding2-tamper", 74)
	allocV1, _ := seventhPassReplacementAllocations(store.StoreID())
	headKey := "finding2-tamper-head"
	rating := seventhPassReplacementWork(t, observation, []economics.AllocationRef{allocV1}, headKey, time.Unix(1_700_313_000, 0).UTC())
	dependency := phase16Finding2SQLiteDependency(t, rating)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, rating))
	rater := &seventhPassEchoAllocationRater{}
	runner, err := billing.NewEconomicJobRunner(economicJobRunnerStoreConfig(store, rater, &economicJobRunnerStoreReconciler{}))
	require.NoError(t, err)
	summary, err := runner.RunOnce(ctx, billing.EconomicQueueProvider)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Completed)

	// Tampered DerivationHash never resolves: the probe misses and the job
	// stays dependency-gated rather than binding a foreign output.
	tampered := dependency
	tampered.DerivationHash = strings.Repeat("f", 64)
	_, err = store.LoadEconomicRevisionDependencyOutput(ctx, tampered)
	require.ErrorIs(t, err, billing.ErrEconomicRevisionDependencyOutputMissing)
	tamperedRecon := phase16Finding2SQLiteReconciliation(t, "test", tampered)
	require.NoError(t, store.AppendEconomicRevisionWork(ctx, tamperedRecon))
	checks, err := store.EconomicRevisionDependencyChecks(ctx, tamperedRecon)
	require.NoError(t, err)
	require.Equal(t, billing.EconomicJobDependencyMissing, checks[0].Status)

	// Missing DerivationHash for an allocation-aware output fails closed: the
	// observation-only key cannot name the allocation-aware valuation.
	missing := dependency
	missing.DerivationHash = ""
	_, err = store.LoadEconomicRevisionDependencyOutput(ctx, missing)
	require.ErrorIs(t, err, billing.ErrEconomicRevisionDependencyOutputMissing)

	// A malformed DerivationHash is rejected at the DTO boundary.
	malformed := dependency
	malformed.DerivationHash = "NOT-A-HASH"
	require.ErrorIs(t, malformed.Validate(), billing.ErrInvalidEconomicRevision)
	_, err = store.LoadEconomicRevisionDependencyOutput(ctx, malformed)
	require.ErrorIs(t, err, billing.ErrInvalidEconomicRevision)
}

func TestPhase16Finding2SQLiteLegacyKeysByteIdentical(t *testing.T) {
	t.Parallel()
	work := economicJobSQLiteWork(t, billing.EconomicQueueProvider, 1, "finding2-legacy-bytes")
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	require.Empty(t, identity.DerivationHash)

	legacy, err := billing.NewEconomicJobDependency(billing.EconomicWorkKindForQueue(normalized.Queue), identity)
	require.NoError(t, err)
	require.Empty(t, legacy.DerivationHash)
	historical := economicJobSQLiteDependency(t, work)
	require.True(t, legacy.Equal(historical))
	require.Equal(t, historical.Key(), legacy.Key())

	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "derivation_hash",
		"observation-only legacy dependency JSON must not gain a derivation field")
}
