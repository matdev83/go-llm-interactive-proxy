package terminalwork

import (
	"fmt"
	"strings"

	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// SourceKey is a stable versioned work identity key (requirement 8.2).
type SourceKey struct {
	IdentityVersion int
	Key             string
}

// Validate rejects non-positive versions and empty/whitespace keys.
func (k SourceKey) Validate() error {
	if k.IdentityVersion < 1 {
		return fmt.Errorf("%w: source key identity version", sdk.ErrInvalid)
	}
	if strings.TrimSpace(k.Key) == "" {
		return fmt.Errorf("%w: empty source key", sdk.ErrInvalid)
	}
	return nil
}

// String returns the stable versioned key encoding.
func (k SourceKey) String() string {
	return fmt.Sprintf("v%d:%s", k.IdentityVersion, k.Key)
}

// BoundVersions records generation/provider/rating versions bound at admit.
type BoundVersions struct {
	GenerationID string
	ProviderID   string
	RatingID     string
}

// LifecycleCorrelation ties work to safe request/attempt identities.
type LifecycleCorrelation struct {
	RequestID string
	AttemptID string
	TraceID   string
}
