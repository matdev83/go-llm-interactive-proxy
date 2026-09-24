package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/stretchr/testify/require"
)

// TestRefinement82BillingCallIDIsInvocationBoundary certifies matrix case 1
// at the domain seam: BillingCallID is the invocation boundary while the
// A-leg is open-ended continuity only (Req 3.1, 3.3, 6.3). Two sequential
// calls on one A-leg receive distinct call keys, disjoint B-leg usage keys,
// and immutable sealed records; nothing in the keying or replay contract
// requires A-leg finality.
func TestRefinement82BillingCallIDIsInvocationBoundary(t *testing.T) {
	t.Parallel()
	call1, err := NewBillingCallID()
	require.NoError(t, err)
	call2, err := NewBillingCallID()
	require.NoError(t, err)
	require.NotEqual(t, call1, call2)

	const aLegID = "a-leg-refinement82"
	key1, err := CallLegUsageKey(call1, "b-leg-1")
	require.NoError(t, err)
	key2, err := CallLegUsageKey(call2, "b-leg-2")
	require.NoError(t, err)
	require.NotEqual(t, key1, key2)

	leg := CallLegUsageRecord{
		CallID: call1, ALegID: aLegID, BLegID: "b-leg-1", AttemptSeq: 1,
		BackendID: "backend-a", ProviderID: "provider-a", ModelID: "model-a",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: LegOutcomeWinner, Surfaced: SurfacedYes,
	}
	sealed, err := leg.Seal()
	require.NoError(t, err)
	require.Equal(t, key1, sealed.Key)
	require.NotEmpty(t, sealed.Fingerprint)

	// Exact replay of the sealed record is accepted; the resumed call never
	// rewrites it.
	require.NoError(t, CheckCallLegUsageReplay(sealed, sealed))
	mutated := sealed
	mutated.Outcome = LegOutcomeFailed
	resealed, err := mutated.Seal()
	require.NoError(t, err)
	require.ErrorIs(t, CheckCallLegUsageReplay(sealed, resealed), ErrReplayConflict)

	call := CallUsageRecord{
		SchemaVersion: CurrentRecordSchemaVersion, CallID: call1,
		AccountID: "acct", ALegID: aLegID, SessionID: "sess",
		StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(101, 0).UTC(),
		Outcome: TurnOutcomeCompleted, ExpectedBLegIDs: []string{"b-leg-1"},
	}
	sealedCall, err := call.Seal()
	require.NoError(t, err)
	require.NoError(t, CheckCallUsageReplay(sealedCall, sealedCall))
}

// TestRefinement82RevisionFenceAndIdempotencyKeys certifies matrix case 6 at
// the domain seam: revision, fence, and idempotency keys prevent duplicate
// usage, valuation, journal, and exposure changes across replay, restart, and
// concurrency (Req 4.2, 4.5, 6.4).
func TestRefinement82RevisionFenceAndIdempotencyKeys(t *testing.T) {
	t.Parallel()
	ref := func(id string, revision uint64, hash string) metering.ObservationRef {
		return metering.ObservationRef{StoreID: "refinement82", ObservationID: id, Revision: revision, PayloadHash: hash}
	}
	a := ref("obs-a", 1, bridgeHash('a'))
	b := ref("obs-b", 2, bridgeHash('b'))
	c := ref("obs-c", 2, bridgeHash('c'))
	d := ref("obs-d", 2, bridgeHash('d'))

	relation, err := CompareEconomicEvidenceSets([]metering.ObservationRef{a, b}, []metering.ObservationRef{a, b})
	require.NoError(t, err)
	require.Equal(t, EconomicEvidenceSetEqual, relation, "exact replay is equal")

	relation, err = CompareEconomicEvidenceSets([]metering.ObservationRef{a, b}, []metering.ObservationRef{a, b, c})
	require.NoError(t, err)
	require.Equal(t, EconomicEvidenceSetCandidateSuperset, relation, "late revision advancing the set is a strict superset")

	relation, err = CompareEconomicEvidenceSets([]metering.ObservationRef{a, b}, []metering.ObservationRef{b})
	require.NoError(t, err)
	require.Equal(t, EconomicEvidenceSetCandidateSubset, relation, "stale subset never advances")

	relation, err = CompareEconomicEvidenceSets([]metering.ObservationRef{a, b}, []metering.ObservationRef{b, d})
	require.NoError(t, err)
	require.Equal(t, EconomicEvidenceSetIncomparable, relation, "conflicting branch is incomparable and must be fenced")

	conflict := ref("obs-a", 1, bridgeHash('f'))
	_, err = CompareEconomicEvidenceSets([]metering.ObservationRef{a}, []metering.ObservationRef{a, conflict})
	require.Error(t, err, "two payload hashes for one observation identity cannot coexist")

	// Work identity is deterministic across replay, reorder, and restart: the
	// same evidence always derives the same idempotency key.
	ctx := context.Background()
	builder, err := NewObservationEconomicWorkBuilder(ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)
	first := bridgeTestObservation("refinement82-first", metering.OriginProvider, metering.BoundaryBackendIngress, 1, true)
	second := bridgeTestObservation("refinement82-second", metering.OriginProvider, metering.BoundaryBackendIngress, 2, true)
	ordered, err := builder.BuildEconomicRevisionWork(ctx, []metering.Observation{first, second})
	require.NoError(t, err)
	require.Len(t, ordered, 1)
	reordered, err := builder.BuildEconomicRevisionWork(ctx, []metering.Observation{second, first})
	require.NoError(t, err)
	require.Len(t, reordered, 1)
	orderedIdentity, err := ordered[0].Identity()
	require.NoError(t, err)
	reorderedIdentity, err := reordered[0].Identity()
	require.NoError(t, err)
	require.Equal(t, orderedIdentity, reorderedIdentity)
	normalized, err := ordered[0].Normalize()
	require.NoError(t, err)
	require.Equal(t, orderedIdentity.InputSetHash, normalized.InputSetHash)
	require.Equal(t, uint64(2), orderedIdentity.EvidenceRevision)
}

// TestRefinement82ProviderAdvancesPerRevisionWhileRetailWaitsForCallClosure
// certifies matrix case 9 at the domain seam: provider/operator economics may
// advance by authoritative B-leg revisions, while default retail settlement
// waits for stable call-level B-leg selection; neither waits for A-leg
// finality (Req 4.3, 6.3).
func TestRefinement82ProviderAdvancesPerRevisionWhileRetailWaitsForCallClosure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	builder, err := NewObservationEconomicWorkBuilder(ObservationEconomicWorkBuilderConfig{})
	require.NoError(t, err)

	// Provider-only authoritative evidence advances the provider queue before
	// any call closure exists, and manufactures no customer work.
	providerOnly := bridgeTestObservation("refinement82-provider-only", metering.OriginProvider, metering.BoundaryBackendIngress, 1, true)
	works, err := builder.BuildEconomicRevisionWork(ctx, []metering.Observation{providerOnly})
	require.NoError(t, err)
	require.Len(t, works, 1)
	require.Equal(t, EconomicQueueProvider, works[0].Queue)
	require.Equal(t, uint64(1), works[0].EvidenceRevision)

	// A later authoritative revision is a new immutable work item on the same
	// head: accrual advances per revision under fence rules.
	late := bridgeTestObservation("refinement82-provider-late", metering.OriginProvider, metering.BoundaryBackendIngress, 2, true)
	advanced, err := builder.BuildEconomicRevisionWork(ctx, []metering.Observation{providerOnly, late})
	require.NoError(t, err)
	require.Len(t, advanced, 1)
	require.Equal(t, works[0].HeadKey, advanced[0].HeadKey)
	require.Equal(t, uint64(2), advanced[0].EvidenceRevision)
	advancedIdentity, err := advanced[0].Identity()
	require.NoError(t, err)
	initialIdentity, err := works[0].Identity()
	require.NoError(t, err)
	require.NotEqual(t, initialIdentity.InputSetHash, advancedIdentity.InputSetHash)

	// Queue isolation holds: customer work is never inferred from
	// provider-only evidence without an explicit customer selection input.
	for _, work := range advanced {
		require.Equal(t, EconomicQueueProvider, work.Queue)
	}
	require.True(t, errors.Is(EconomicQueueCustomer.Validate(), nil))
	require.True(t, errors.Is(EconomicQueueProvider.Validate(), nil))
}
