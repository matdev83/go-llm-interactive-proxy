package configsource

import (
	"bytes"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ClassifyBytes classifies a raw snapshot against the bounded source contract
// before typed YAML decode. It does not decode YAML; empty/whitespace/oversize
// are rejected here so decode never sees them.
//
// maxBytes <= 0 selects DefaultMaxBytes. Classification is secret-safe: errors
// never include the raw payload.
func ClassifyBytes(raw []byte, maxBytes int64) (Category, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if int64(len(raw)) > maxBytes {
		return CategoryOversize, fmt.Errorf("configsource: %s (limit %d bytes)", CategoryOversize, maxBytes)
	}
	if len(raw) == 0 {
		return CategoryEmpty, fmt.Errorf("configsource: %s", CategoryEmpty)
	}
	if isWhitespaceOnly(raw) {
		return CategoryWhitespace, fmt.Errorf("configsource: %s", CategoryWhitespace)
	}
	return CategoryOK, nil
}

func isWhitespaceOnly(raw []byte) bool {
	i := 0
	for i < len(raw) {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			// Non-UTF8 bytes are not whitespace; leave to decode.
			return false
		}
		if !unicode.IsSpace(r) {
			return false
		}
		i += size
	}
	return true
}

// ClassifyPathPresence maps filesystem presence to a source category without
// reading contents. Used by table fixtures and the future ReadStable path.
func ClassifyPathPresence(exists, isRegular bool) (Category, error) {
	if !exists {
		return CategoryMissing, fmt.Errorf("configsource: %s", CategoryMissing)
	}
	if !isRegular {
		return CategoryUnsupportedType, fmt.Errorf("configsource: %s", CategoryUnsupportedType)
	}
	return CategoryOK, nil
}

// ClassifyStability rejects torn/partial reads where handle metadata changed
// across the bounded read window (requirement 2.1–2.2).
func ClassifyStability(beforeSize, afterSize int64, beforeID, afterID FileIdentity) (Category, error) {
	if beforeSize != afterSize || beforeID != afterID {
		return CategoryUnstable, fmt.Errorf("configsource: %s", CategoryUnstable)
	}
	return CategoryOK, nil
}

// ClassifyAtomicReplacement enforces requirement 2.9: changed content with the
// same handle identity is a non-atomic in-place update; identical digest may
// no-op; a new identity is eligible for reload.
type AtomicResult string

const (
	AtomicEligible AtomicResult = "eligible"
	AtomicNoop     AtomicResult = "noop"
	AtomicReject   AtomicResult = "reject_non_atomic"
)

func ClassifyAtomicReplacement(active ActiveSourceVersion, candidate SourceSnapshot) (AtomicResult, Category, error) {
	sameID := active.HandleIdentity == candidate.HandleIdentity
	sameDigest := bytes.Equal(active.PrivateDigest[:], candidate.PrivateDigest[:])
	switch {
	case sameID && sameDigest:
		return AtomicNoop, CategoryOK, nil
	case sameID && !sameDigest:
		return AtomicReject, CategoryNonAtomicUpdate, fmt.Errorf("configsource: %s", CategoryNonAtomicUpdate)
	default:
		return AtomicEligible, CategoryOK, nil
	}
}
