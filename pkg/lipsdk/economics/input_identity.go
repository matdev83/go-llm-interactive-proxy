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

var (
	// ErrInputSetHashMismatch identifies a caller-supplied input identity that
	// does not describe the canonical observation reference set. It is kept as
	// a typed sentinel so direct raters and durable stores classify the same
	// trust-boundary violation deterministically.
	ErrInputSetHashMismatch = errors.New("economics: input set hash mismatch")
)

// CanonicalInputSetHash returns the SHA-256 identity of one valuation basis
// and its canonical observation-reference set. Exact duplicate references are
// collapsed, while two payload hashes for one observation revision are
// rejected because they cannot describe one immutable input set.
//
// Callers must verify a supplied non-empty hash against this result before
// using it as an in-memory or durable identity. An empty caller hash is not an
// error; the trusted boundary may fill it with this result.
func CanonicalInputSetHash(basis ValuationBasis, refs []metering.ObservationRef) (string, error) {
	ordered := append([]metering.ObservationRef(nil), refs...)
	for i, ref := range ordered {
		if err := ref.Validate(); err != nil {
			return "", fmt.Errorf("economics: input observation ref %d: %v", i, err)
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
				return "", fmt.Errorf("%w: conflicting payload hashes for %s/%s revision %d", ErrInputSetHashMismatch, ref.StoreID, ref.ObservationID, ref.Revision)
			}
			continue
		}
		seen[identity] = ref.PayloadHash
		canonical = append(canonical, ref)
	}
	encoded, err := json.Marshal(struct {
		Basis ValuationBasis            `json:"basis"`
		Refs  []metering.ObservationRef `json:"refs"`
	}{Basis: basis, Refs: canonical})
	if err != nil {
		return "", fmt.Errorf("economics: input identity preimage: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
