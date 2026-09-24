package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// ErrInputSetHashMismatch identifies a caller-supplied input identity that
// does not describe the canonical observation reference set. It is kept as
// a typed sentinel so direct raters and durable stores classify the same
// trust-boundary violation deterministically.
var ErrInputSetHashMismatch = errors.New("economics: input set hash mismatch")

// CanonicalInputSetHash returns the SHA-256 identity of one valuation basis
// and its canonical observation-reference set. Exact duplicate references are
// collapsed, while two payload hashes for one observation revision are
// rejected because they cannot describe one immutable input set.
//
// Callers must verify a supplied non-empty hash against this result before
// using it as an in-memory or durable identity. An empty caller hash is not an
// error; the trusted boundary may fill it with this result.
func CanonicalInputSetHash(basis ValuationBasis, refs []metering.ObservationRef) (string, error) {
	return CanonicalValuationInputSetHash(basis, refs, nil)
}

// CanonicalValuationInputSetHash returns the SHA-256 identity of one valuation
// basis, its canonical observation-reference set and its canonical allocation
// coverage-reference set. Allocation coverage references are economically
// significant valuation inputs: the same observations, rater, policy and
// tariff can be re-priced by re-proving a different immutable allocation
// revision, so a durable revision identity that omitted them could not persist
// a valid correction.
//
// The allocation set is canonicalized by sorting and collapsing exact
// duplicates, mirroring observation handling. When no allocation coverage is
// present the preimage is byte-for-byte identical to the historical
// CanonicalInputSetHash, so legacy durable identity, replay and fingerprints
// are unchanged.
func CanonicalValuationInputSetHash(basis ValuationBasis, refs []metering.ObservationRef, allocationRefs []AllocationRef) (string, error) {
	canonical, err := canonicalObservationRefs(refs)
	if err != nil {
		return "", err
	}
	allocations, err := canonicalAllocationCoverageRefs(allocationRefs)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Basis          ValuationBasis            `json:"basis"`
		Refs           []metering.ObservationRef `json:"refs"`
		AllocationRefs []AllocationRef           `json:"allocation_coverage_refs,omitempty"`
	}{Basis: basis, Refs: canonical, AllocationRefs: allocations})
	if err != nil {
		return "", fmt.Errorf("economics: valuation input identity preimage: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalObservationRefs(refs []metering.ObservationRef) ([]metering.ObservationRef, error) {
	ordered := append([]metering.ObservationRef(nil), refs...)
	for i, ref := range ordered {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("economics: input observation ref %d: %v", i, err)
		}
	}
	slices.SortFunc(ordered, func(a, b metering.ObservationRef) int {
		if a.StoreID != b.StoreID {
			return strings.Compare(a.StoreID, b.StoreID)
		}
		if a.ObservationID != b.ObservationID {
			return strings.Compare(a.ObservationID, b.ObservationID)
		}
		if a.Revision != b.Revision {
			if a.Revision < b.Revision {
				return -1
			}
			return 1
		}
		return strings.Compare(a.PayloadHash, b.PayloadHash)
	})
	canonical := ordered[:0]
	type revisionIdentity struct {
		store, observation string
		revision           uint64
	}
	seen := make(map[revisionIdentity]string, len(ordered))
	for _, ref := range ordered {
		identity := revisionIdentity{store: ref.StoreID, observation: ref.ObservationID, revision: ref.Revision}
		if prior, exists := seen[identity]; exists {
			if prior != ref.PayloadHash {
				return nil, fmt.Errorf("%w: conflicting payload hashes for %s/%s revision %d", ErrInputSetHashMismatch, ref.StoreID, ref.ObservationID, ref.Revision)
			}
			continue
		}
		seen[identity] = ref.PayloadHash
		canonical = append(canonical, ref)
	}
	return canonical, nil
}

// CanonicalAllocationCoverageRefs validates, sorts and collapses exact
// duplicate allocation coverage references into their canonical set. Two
// payload hashes for one store/allocation/version identity are rejected because
// they cannot describe one immutable input set.
func CanonicalAllocationCoverageRefs(refs []AllocationRef) ([]AllocationRef, error) {
	return canonicalAllocationCoverageRefs(refs)
}

func canonicalAllocationCoverageRefs(refs []AllocationRef) ([]AllocationRef, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	ordered := append([]AllocationRef(nil), refs...)
	for i, ref := range ordered {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("economics: allocation coverage ref %d: %v", i, err)
		}
	}
	slices.SortFunc(ordered, func(a, b AllocationRef) int {
		return strings.Compare(allocationRefSortKey(a), allocationRefSortKey(b))
	})
	canonical := ordered[:0]
	type revisionIdentity struct {
		store, allocation string
		version           uint64
	}
	seen := make(map[revisionIdentity]string, len(ordered))
	for _, ref := range ordered {
		identity := revisionIdentity{store: ref.StoreID, allocation: ref.AllocationID, version: ref.Version}
		if prior, exists := seen[identity]; exists {
			if prior != ref.PayloadHash {
				return nil, fmt.Errorf("%w: conflicting payload hashes for allocation %s/%s version %d", ErrInputSetHashMismatch, ref.StoreID, ref.AllocationID, ref.Version)
			}
			continue
		}
		seen[identity] = ref.PayloadHash
		canonical = append(canonical, ref)
	}
	return canonical, nil
}
