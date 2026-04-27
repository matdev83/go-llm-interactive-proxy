package auth

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MinLocalAPIKeyRunes is the minimum accepted length (in Unicode code points) for [LocalAPIKeyRecord.Key].
// Shorter keys are rejected at validation to reduce trivial online guessing when the listener is exposed.
const MinLocalAPIKeyRunes = 16

// LocalAPIKeyRecord is one operator-configured API key for [LocalAPIKeyAuthenticator].
// It mirrors config-layer YAML records without importing internal/core/config.
type LocalAPIKeyRecord struct {
	KeyID       string
	PrincipalID string
	Key         string
}

// ValidateLocalAPIKeyRecords checks records for duplicates and required fields.
func ValidateLocalAPIKeyRecords(records []LocalAPIKeyRecord) error {
	seen := make(map[string]struct{}, len(records))
	seenSecrets := make(map[string]struct{}, len(records))
	for i, r := range records {
		kid := strings.TrimSpace(r.KeyID)
		pid := strings.TrimSpace(r.PrincipalID)
		key := strings.TrimSpace(r.Key)
		if kid == "" && pid == "" && key == "" {
			return fmt.Errorf("auth.local_api_keys[%d]: empty record (requires key_id, principal_id, and key)", i)
		}
		if kid == "" {
			return fmt.Errorf("auth.local_api_keys[%d]: key_id is required", i)
		}
		if pid == "" {
			return fmt.Errorf("auth.local_api_keys[%d]: principal_id is required for key_id %q", i, kid)
		}
		if key == "" {
			return fmt.Errorf("%w: index %d key_id %q", ErrLocalAPIKeyEmpty, i, kid)
		}
		if n := utf8.RuneCountInString(key); n < MinLocalAPIKeyRunes {
			return fmt.Errorf(
				"auth.local_api_keys[%d]: key for key_id %q must be at least %d characters (got %d)",
				i, kid, MinLocalAPIKeyRunes, n,
			)
		}
		if _, dup := seen[kid]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateLocalAPIKeyID, kid)
		}
		seen[kid] = struct{}{}
		if _, dup := seenSecrets[key]; dup {
			return fmt.Errorf("%w: key material reused for a different key_id is not allowed", ErrDuplicateLocalAPIKeyMaterial)
		}
		seenSecrets[key] = struct{}{}
	}
	return nil
}
