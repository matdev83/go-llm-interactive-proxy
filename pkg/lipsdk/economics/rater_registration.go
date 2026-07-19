package economics

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// RaterRegistration binds a stable rater identity, economic perspective, and
// rater instance for production composition (design D4 / D15).
type RaterRegistration struct {
	ID          string
	Perspective metering.EconomicPerspective
	Rater       Rater
}

// Validate checks identity, perspective, and rater instance binding.
func (r RaterRegistration) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("economics: rater registration id required")
	}
	if err := r.Perspective.Validate(); err != nil {
		return fmt.Errorf("economics: rater registration perspective: %w", err)
	}
	if isNilValue(r.Rater) {
		return fmt.Errorf("economics: rater registration rater required")
	}
	return nil
}
