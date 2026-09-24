package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 sixth-pass Finding 1 core boundary proof: the pure revision
// normalizer must derive the stored valuation input identity from the complete
// economic input set, including allocation coverage references. Observation
// equivalence against the immutable work envelope is still governed by the
// observation-only identity so a trusted rater cannot silently change the
// evidence plane.

func phase16Finding1BaseValuation(t *testing.T, work EconomicRevisionWork, identity EconomicRevisionIdentity) economics.Valuation {
	t.Helper()
	return economics.Valuation{
		ID:                identity.ValuationKey(),
		Version:           economics.ValuationVersionV2,
		Perspective:       work.Input.Perspective,
		Basis:             work.Input.Basis,
		Subject:           work.Input.Subject,
		Scope:             work.Input.Scope,
		InputObservations: append([]metering.ObservationRef(nil), work.Input.ObservationRefs...),
		Completeness:      economics.CompletenessPartial,
		CreatedAt:         work.CreatedAt,
	}
}

func TestPhase16Finding1NormalizeRevisionValuationIncludesAllocationCoverage(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 1, "3")
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)

	base := phase16Finding1BaseValuation(t, normalized, identity)

	noAllocations, err := normalizeRevisionValuation(normalized, identity, base)
	require.NoError(t, err)
	require.Equal(t, normalized.InputSetHash, noAllocations.InputSetHash,
		"without allocation coverage the stored input identity must match the immutable work identity")

	allocV1 := economics.AllocationRef{StoreID: normalized.Subject.StoreID, AllocationID: "alloc-f1", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocV2 := economics.AllocationRef{StoreID: normalized.Subject.StoreID, AllocationID: "alloc-f1", Version: 2, PayloadHash: strings.Repeat("b", 64)}

	withV1 := base.Clone()
	withV1.AllocationCoverageRefs = []economics.AllocationRef{allocV1}
	normalizedV1, err := normalizeRevisionValuation(normalized, identity, withV1)
	require.NoError(t, err)
	require.NotEqual(t, normalized.InputSetHash, normalizedV1.InputSetHash,
		"allocation coverage must revise the stored valuation input identity")

	withV2 := base.Clone()
	withV2.AllocationCoverageRefs = []economics.AllocationRef{allocV2}
	normalizedV2, err := normalizeRevisionValuation(normalized, identity, withV2)
	require.NoError(t, err)
	require.NotEqual(t, normalizedV1.InputSetHash, normalizedV2.InputSetHash,
		"a replacement allocation revision must revise the stored valuation input identity")

	withBoth := base.Clone()
	withBoth.AllocationCoverageRefs = []economics.AllocationRef{allocV1, allocV2}
	normalizedBoth, err := normalizeRevisionValuation(normalized, identity, withBoth)
	require.NoError(t, err)
	withBothReordered := base.Clone()
	withBothReordered.AllocationCoverageRefs = []economics.AllocationRef{allocV2, allocV1}
	normalizedReordered, err := normalizeRevisionValuation(normalized, identity, withBothReordered)
	require.NoError(t, err)
	require.Equal(t, normalizedBoth.InputSetHash, normalizedReordered.InputSetHash,
		"allocation coverage order must not change the derived input identity")
}

// TestPhase16Finding1NormalizeRevisionValuationStillFencesObservationDrift proves
// the work-envelope fence stays on the observation identity: a rater that
// changes the evidence plane is rejected even when allocation coverage is
// present.
func TestPhase16Finding1NormalizeRevisionValuationStillFencesObservationDrift(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 1, "3")
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)

	drifted := phase16Finding1BaseValuation(t, normalized, identity)
	drifted.InputObservations = append([]metering.ObservationRef(nil), drifted.InputObservations...)
	drifted.InputObservations[0].ObservationID += "-drifted"
	drifted.AllocationCoverageRefs = []economics.AllocationRef{{
		StoreID: normalized.Subject.StoreID, AllocationID: "alloc-f1", Version: 1, PayloadHash: strings.Repeat("a", 64),
	}}

	_, err = normalizeRevisionValuation(normalized, identity, drifted)
	require.ErrorIs(t, err, ErrEconomicRevisionInputMismatch)
	var mismatch *EconomicRevisionInputMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, normalized.InputSetHash, mismatch.Expected)
	require.NotEqual(t, mismatch.Expected, mismatch.Actual)
}
