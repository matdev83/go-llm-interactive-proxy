package economics

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// OutputLimitStatus classifies money→output-token conversion without conflating
// deterministic capacity exhaustion with unsupported or overflowed pricing
// (requirement 6.5).
type OutputLimitStatus string

const (
	// OutputLimitOK means MaxOutputTokens is an enforceable bound.
	OutputLimitOK OutputLimitStatus = "ok"
	// OutputLimitCapacityExhausted means fixed quantities already consume the
	// monetary cap (or leave zero remaining output budget).
	OutputLimitCapacityExhausted OutputLimitStatus = "capacity_exhausted"
	// OutputLimitUnsupported means the rater cannot convert this request
	// (missing rates, non-invertible offer, etc.).
	OutputLimitUnsupported OutputLimitStatus = "unsupported"
	// OutputLimitOverflow means checked arithmetic could not represent the bound.
	OutputLimitOverflow OutputLimitStatus = "overflow"
)

// OutputLimitRequest asks a rater to convert a monetary spend cap into a
// maximum output-token bound after accounting for fixed quantities (typically
// already-committed input). This is the public inverse of Rate used by
// admission clamps (requirements 6.1, 6.5, 7.4).
type OutputLimitRequest struct {
	Perspective     metering.EconomicPerspective `json:"perspective"`
	BackendID       string                       `json:"backend_id,omitempty"`
	Model           string                       `json:"model,omitempty"`
	FrontendID      string                       `json:"frontend_id,omitempty"`
	FixedQuantities []metering.Quantity          `json:"fixed_quantities,omitempty"`
	MaxMoney        Money                        `json:"max_money"`
	At              time.Time                    `json:"at,omitempty"`
}

// OutputLimitResult is one perspective-specific money→token bound with provenance.
type OutputLimitResult struct {
	Status          OutputLimitStatus            `json:"status"`
	MaxOutputTokens int64                        `json:"max_output_tokens,omitempty"`
	Source          string                       `json:"source,omitempty"`
	Authority       string                       `json:"authority,omitempty"`
	Version         VersionRef                   `json:"version"`
	EffectiveAt     time.Time                    `json:"effective_at,omitempty"`
	RoundingPolicy  RoundingPolicy               `json:"rounding_policy,omitempty"`
	Perspective     metering.EconomicPerspective `json:"perspective"`
	RaterID         string                       `json:"rater_id,omitempty"`
	Reason          string                       `json:"reason,omitempty"`
}

// OutputLimitQuoter converts monetary spend caps into enforceable output-token
// bounds. Closed raters that can Rate but cannot invert money→tokens must
// return Status=unsupported (or an error) rather than inventing a bound.
// When a Rater is injected into the runtime, clamp conversion uses this
// contract exclusively — never a silent catalog fallback (requirements 6.1–6.5, 12.1).
type OutputLimitQuoter interface {
	QuoteOutputLimit(ctx context.Context, req OutputLimitRequest) (OutputLimitResult, error)
}
