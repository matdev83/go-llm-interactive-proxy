package economics_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// Phase 16 sixth-pass Finding 1 SDK boundary proof: allocation coverage
// references are economically significant valuation inputs. A valuation may be
// corrected solely by re-proving a different immutable allocation revision
// while its observations, rater, policy and tariff stay fixed, so the canonical
// valuation input identity must change. An empty allocation-coverage set must
// preserve the historical CanonicalInputSetHash byte-for-byte.

func phase16Finding1ObservationRefs() []metering.ObservationRef {
	return []metering.ObservationRef{{
		StoreID: "store-1", ObservationID: "obs-1", Revision: 1, PayloadHash: strings.Repeat("1", 64),
	}}
}

func TestPhase16Finding1CanonicalValuationInputIdentityIncludesAllocationCoverage(t *testing.T) {
	t.Parallel()
	basis := economics.BasisProviderReported
	refs := phase16Finding1ObservationRefs()

	legacy, err := economics.CanonicalInputSetHash(basis, refs)
	require.NoError(t, err)

	noAllocations, err := economics.CanonicalValuationInputSetHash(basis, refs, nil)
	require.NoError(t, err)
	require.Equal(t, legacy, noAllocations, "a nil allocation set must preserve the legacy input identity byte-for-byte")

	empty, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{})
	require.NoError(t, err)
	require.Equal(t, legacy, empty, "an empty allocation set must preserve the legacy input identity byte-for-byte")

	allocV1 := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-1", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocV2 := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-1", Version: 2, PayloadHash: strings.Repeat("b", 64)}

	withV1, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{allocV1})
	require.NoError(t, err)
	require.NotEqual(t, noAllocations, withV1, "allocation coverage must participate in the canonical input identity")

	withV2, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{allocV2})
	require.NoError(t, err)
	require.NotEqual(t, withV1, withV2, "a replacement allocation revision must revise the canonical input identity")

	both, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{allocV1, allocV2})
	require.NoError(t, err)
	reordered, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{allocV2, allocV1})
	require.NoError(t, err)
	require.Equal(t, both, reordered, "allocation coverage order must not change the canonical input identity")

	duplicated, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{allocV1, allocV1, allocV2})
	require.NoError(t, err)
	require.Equal(t, both, duplicated, "exact duplicate allocation coverage refs must collapse canonically")
}

func TestPhase16Finding1CanonicalValuationInputIdentityRejectsConflictingAllocations(t *testing.T) {
	t.Parallel()
	basis := economics.BasisProviderReported
	refs := phase16Finding1ObservationRefs()
	allocV1 := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-1", Version: 1, PayloadHash: strings.Repeat("a", 64)}

	// One immutable allocation revision cannot carry two payload hashes.
	conflicting := allocV1
	conflicting.PayloadHash = strings.Repeat("f", 64)
	_, err := economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{allocV1, conflicting})
	require.ErrorIs(t, err, economics.ErrInputSetHashMismatch)

	// Structurally invalid allocation coverage references are rejected rather
	// than silently dropped from the identity.
	invalidVersion := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-1", Version: 0, PayloadHash: strings.Repeat("a", 64)}
	_, err = economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{invalidVersion})
	require.Error(t, err)

	invalidHash := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-1", Version: 1, PayloadHash: "not-sha256"}
	_, err = economics.CanonicalValuationInputSetHash(basis, refs, []economics.AllocationRef{invalidHash})
	require.Error(t, err)

	// Duplicate allocation coverage references are rejected by valuation
	// validation even though the pure identity collapses exact duplicates.
	duplicate := validValuation(economics.BasisProviderReported)
	duplicate.AllocationCoverageRefs = []economics.AllocationRef{allocV1, allocV1}
	require.Error(t, duplicate.Validate())
}

// TestPhase16SeventhPassAllocationClaimContract proves the immutable work-input
// allocation claim contract: the canonical helper is order-insensitive,
// collapses exact duplicates, rejects conflicting payloads for one allocation
// revision, and the rating input detaches its declared allocation set on Clone.
func TestPhase16SeventhPassAllocationClaimContract(t *testing.T) {
	t.Parallel()
	allocA := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-a", Version: 1, PayloadHash: strings.Repeat("a", 64)}
	allocB := economics.AllocationRef{StoreID: "store-1", AllocationID: "alloc-b", Version: 1, PayloadHash: strings.Repeat("b", 64)}

	canonical, err := economics.CanonicalAllocationCoverageRefs([]economics.AllocationRef{allocB, allocA, allocA})
	require.NoError(t, err)
	require.Equal(t, []economics.AllocationRef{allocA, allocB}, canonical,
		"the canonical allocation set is order-insensitive and collapses exact duplicates")

	conflicting := allocA
	conflicting.PayloadHash = strings.Repeat("f", 64)
	_, err = economics.CanonicalAllocationCoverageRefs([]economics.AllocationRef{allocA, conflicting})
	require.ErrorIs(t, err, economics.ErrInputSetHashMismatch,
		"one allocation revision cannot carry two payload hashes")

	input := economics.RatingInput{AllocationCoverageRefs: []economics.AllocationRef{allocA}}
	clone := input.Clone()
	clone.AllocationCoverageRefs[0].PayloadHash = strings.Repeat("f", 64)
	require.Equal(t, strings.Repeat("a", 64), input.AllocationCoverageRefs[0].PayloadHash,
		"RatingInput.Clone must detach the declared allocation set")
}
