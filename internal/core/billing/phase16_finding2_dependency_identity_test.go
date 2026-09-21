package billing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 eighth-pass Finding 2 core identity proof: the dependency DTO must
// carry the full allocation-aware derivation identity so OutputIdentity
// reconstructs the exact EconomicRevisionIdentity used by the producer.
// Legacy observation-only dependencies keep their byte-identical keys.

func phase16Finding2AllocationWork(t *testing.T, allocation economics.AllocationRef) EconomicRevisionWork {
	t.Helper()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 1, "3")
	work.HeadKey = "finding2-dependency-head"
	normalized, err := work.Normalize()
	require.NoError(t, err)
	allocation.StoreID = normalized.Subject.StoreID
	work.Input.AllocationCoverageRefs = []economics.AllocationRef{allocation}
	return work
}

func phase16Finding2AllocationRef(id string, version uint64, fill string) economics.AllocationRef {
	return economics.AllocationRef{
		StoreID: "finding2-store", AllocationID: id, Version: version, PayloadHash: strings.Repeat(fill, 64),
	}
}

func TestPhase16Finding2DependencyCarriesAllocationDerivation(t *testing.T) {
	t.Parallel()
	allocV1 := phase16Finding2AllocationRef("alloc-finding2", 1, "a")
	allocV2 := phase16Finding2AllocationRef("alloc-finding2", 2, "b")

	first := phase16Finding2AllocationWork(t, allocV1)
	second := phase16Finding2AllocationWork(t, allocV1)
	replacement := phase16Finding2AllocationWork(t, allocV2)

	firstNorm, err := first.Normalize()
	require.NoError(t, err)
	firstID, err := firstNorm.Identity()
	require.NoError(t, err)
	require.NotEmpty(t, firstID.DerivationHash, "allocation-aware producer must bind a derivation hash")

	secondNorm, err := second.Normalize()
	require.NoError(t, err)
	secondID, err := secondNorm.Identity()
	require.NoError(t, err)
	require.Equal(t, firstID, secondID)

	replacementNorm, err := replacement.Normalize()
	require.NoError(t, err)
	replacementID, err := replacementNorm.Identity()
	require.NoError(t, err)
	require.NotEqual(t, firstID.ValuationKey(), replacementID.ValuationKey(),
		"allocation-only replacement must be a distinct producer output")

	firstDep, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, firstID)
	require.NoError(t, err)
	require.Equal(t, firstID.DerivationHash, firstDep.DerivationHash)
	require.Equal(t, firstID.InputSetHash, firstDep.InputSetHash)

	secondDep, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, secondID)
	require.NoError(t, err)
	require.True(t, firstDep.Equal(secondDep))
	require.Equal(t, firstDep.Key(), secondDep.Key())

	replacementDep, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, replacementID)
	require.NoError(t, err)
	require.False(t, firstDep.Equal(replacementDep))
	require.NotEqual(t, firstDep.Key(), replacementDep.Key(),
		"allocation-only replacement must be a distinct dependency/output identity")
	firstOutputCheck, err := firstDep.OutputIdentity()
	require.NoError(t, err)
	replacementOutputCheck, err := replacementDep.OutputIdentity()
	require.NoError(t, err)
	require.NotEqual(t, firstOutputCheck.ValuationKey(), replacementOutputCheck.ValuationKey())

	firstOutput, err := firstDep.OutputIdentity()
	require.NoError(t, err)
	require.Equal(t, firstID, firstOutput,
		"OutputIdentity must reconstruct the exact producer revision identity")
	require.Equal(t, firstID.ValuationKey(), firstOutput.ValuationKey())
}

func TestPhase16Finding2DependencyReorderedRefsIdempotent(t *testing.T) {
	t.Parallel()
	allocA := phase16Finding2AllocationRef("alloc-reorder-a", 1, "c")
	allocB := phase16Finding2AllocationRef("alloc-reorder-b", 1, "d")
	base := economicRevisionTestWork(t, EconomicQueueProvider, 1, "3")
	base.HeadKey = "finding2-reorder-head"
	normalized, err := base.Normalize()
	require.NoError(t, err)
	allocA.StoreID = normalized.Subject.StoreID
	allocB.StoreID = normalized.Subject.StoreID

	ordered := base
	ordered.Input.AllocationCoverageRefs = []economics.AllocationRef{allocA, allocB}
	reordered := base
	reordered.Input.AllocationCoverageRefs = []economics.AllocationRef{allocB, allocA}

	orderedNorm, err := ordered.Normalize()
	require.NoError(t, err)
	orderedID, err := orderedNorm.Identity()
	require.NoError(t, err)
	reorderedNorm, err := reordered.Normalize()
	require.NoError(t, err)
	reorderedID, err := reorderedNorm.Identity()
	require.NoError(t, err)
	require.Equal(t, orderedID, reorderedID)

	orderedDep, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, orderedID)
	require.NoError(t, err)
	reorderedDep, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, reorderedID)
	require.NoError(t, err)
	require.True(t, orderedDep.Equal(reorderedDep))
	require.Equal(t, orderedDep.Key(), reorderedDep.Key())
}

func TestPhase16Finding2DependencyValidationRejectsTamperedIdentity(t *testing.T) {
	t.Parallel()
	alloc := phase16Finding2AllocationRef("alloc-finding2-tamper", 1, "e")
	work := phase16Finding2AllocationWork(t, alloc)
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)

	dependency, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, identity)
	require.NoError(t, err)

	// Full allocation-aware valuation for the producer identity.
	valuation := economics.Valuation{
		ID: identity.ValuationKey(), Version: economics.ValuationVersionV2,
		Perspective: normalized.Input.Perspective, Basis: normalized.Input.Basis,
		Subject: normalized.Subject, Scope: normalized.Input.Scope,
		InputObservations:      append([]metering.ObservationRef(nil), normalized.Input.ObservationRefs...),
		AllocationCoverageRefs: append([]economics.AllocationRef(nil), normalized.Input.AllocationCoverageRefs...),
		Completeness:           economics.CompletenessPartial, CreatedAt: normalized.CreatedAt,
	}
	fullHash, err := economics.CanonicalValuationInputSetHash(valuation.Basis, valuation.InputObservations, valuation.AllocationCoverageRefs)
	require.NoError(t, err)
	valuation.InputSetHash = fullHash
	require.NoError(t, validateEconomicJobDependencyOutput(dependency, valuation),
		"exact allocation-aware output must validate")

	// Tampered DerivationHash: a different valid hash never resolves to this output.
	tampered := dependency
	tampered.DerivationHash = strings.Repeat("f", 64)
	require.ErrorIs(t, validateEconomicJobDependencyOutput(tampered, valuation), ErrEconomicRevisionInputMismatch)

	// Missing DerivationHash when the output is allocation-aware: the
	// observation-only key cannot name the allocation-aware valuation and the
	// full-input fence must fail closed, never guess from the observation hash.
	missing := dependency
	missing.DerivationHash = ""
	require.ErrorIs(t, validateEconomicJobDependencyOutput(missing, valuation), ErrEconomicRevisionInputMismatch)

	// Tampered full output hash on the valuation itself.
	drifted := valuation
	drifted.InputSetHash = strings.Repeat("9", 64)
	require.ErrorIs(t, validateEconomicJobDependencyOutput(dependency, drifted), ErrEconomicRevisionInputMismatch)

	// Malformed DerivationHash is rejected at the DTO boundary.
	malformed := dependency
	malformed.DerivationHash = "NOT-A-HASH"
	require.ErrorIs(t, malformed.Validate(), ErrInvalidEconomicRevision)
	_, err = malformed.OutputIdentity()
	require.ErrorIs(t, err, ErrInvalidEconomicRevision)
}

func TestPhase16Finding2LegacyObservationOnlyKeysByteIdentical(t *testing.T) {
	t.Parallel()
	work := economicRevisionTestWork(t, EconomicQueueProvider, 1, "3")
	work.HeadKey = "finding2-legacy-head"
	normalized, err := work.Normalize()
	require.NoError(t, err)
	identity, err := normalized.Identity()
	require.NoError(t, err)
	require.Empty(t, identity.DerivationHash)

	legacy, err := NewEconomicJobDependency(EconomicWorkKindProviderRating, identity)
	require.NoError(t, err)
	require.Empty(t, legacy.DerivationHash)

	output, err := legacy.OutputIdentity()
	require.NoError(t, err)
	require.Empty(t, output.DerivationHash)
	require.Equal(t, identity, output)
	require.Equal(t, identity.Key(), legacy.Key())
	require.Equal(t, identity.ValuationKey(), output.ValuationKey())

	// The durable dependency hash preimage for an observation-only dependency
	// is unchanged: no derivation segment is bound when the field is empty.
	singleHash, err := economicDependenciesHash([]EconomicJobDependency{legacy})
	require.NoError(t, err)
	require.NotEmpty(t, singleHash)
	duplicatedHash, err := economicDependenciesHash([]EconomicJobDependency{legacy, legacy})
	require.NoError(t, err)
	require.Equal(t, singleHash, duplicatedHash)
}
