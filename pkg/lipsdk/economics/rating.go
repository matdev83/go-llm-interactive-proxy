package economics

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// RoundingPolicy identifies how fractional nano-units were resolved.
type RoundingPolicy string

const (
	RoundingUnspecified      RoundingPolicy = ""
	RoundingHalfAwayFromZero RoundingPolicy = "half_away_from_zero"
	RoundingHalfEven         RoundingPolicy = "half_even"
	RoundingTowardZero       RoundingPolicy = "toward_zero"
	RoundingFloor            RoundingPolicy = "floor"
)

// RatingRequest asks a rater to price quantities for one economic perspective.
// Customer and operator rating are independent invocations (requirements 1.4, 4.6, 5.8).
// FactIDs bind journal fact identities for the rated exposure (Phase 1 backend-ingress
// facts when a MeteringRecorder is configured). Output carries the conservative
// output assumption when future output is bounded; omit when unknown.
//
// Compatibility: callers must construct RatingRequest with keyed field literals.
// New fields may be added in minor releases; unkeyed composite literals will not
// compile against newer SDK versions.
type RatingRequest struct {
	Perspective metering.EconomicPerspective `json:"perspective"`
	BackendID   string                       `json:"backend_id,omitempty"`
	Model       string                       `json:"model,omitempty"`
	FrontendID  string                       `json:"frontend_id,omitempty"`
	Quantities  []metering.Quantity          `json:"quantities,omitempty"`
	Output      ConservativeOutputAssumption `json:"output,omitzero"`
	FactIDs     []string                     `json:"fact_ids,omitempty"`
	FactRefs    []metering.FactRef           `json:"fact_refs,omitempty"`
	At          time.Time                    `json:"at,omitzero"`
}

// RatingResult is one perspective-specific rated amount with provenance.
type RatingResult struct {
	Money          Money                        `json:"money"`
	Source         string                       `json:"source,omitempty"`
	Authority      string                       `json:"authority,omitempty"`
	Version        VersionRef                   `json:"version"`
	EffectiveAt    time.Time                    `json:"effective_at,omitzero"`
	LineID         string                       `json:"line_id,omitempty"`
	RoundingPolicy RoundingPolicy               `json:"rounding_policy,omitempty"`
	Perspective    metering.EconomicPerspective `json:"perspective"`
	RaterID        string                       `json:"rater_id,omitempty"`
}

// Rater rates quantities independently per EconomicPerspective on the request.
type Rater interface {
	Rate(ctx context.Context, req RatingRequest) (RatingResult, error)
}
