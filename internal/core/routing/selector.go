package routing

import (
	"net/url"
	"strings"
	"time"
)

// Selector is the root AST: an ordered failover chain (left-to-right).
type Selector struct {
	Alternatives      []FailoverAlt
	GlobalTTFTTimeout *time.Duration
}

// FailoverAlt is one arm of a failover selector (before the next '|').
// Exactly one of Primary or Weighted is non-nil.
type FailoverAlt struct {
	Primary  *Primary
	Weighted *Weighted
}

// Primary is a concrete backend:model (or model-only) with optional query parameters.
type Primary struct {
	// Backend is empty for model-only selectors. It may contain dots (e.g. openai.azure).
	Backend     string
	Model       string
	Params      url.Values
	Size        RequestSizeConstraint
	TTFTTimeout *time.Duration
}

// RequestSizeConstraint carries per-leaf request token eligibility bounds.
type RequestSizeConstraint struct {
	MinContextTokens *int64
	MaxContextTokens *int64
}

// Weighted is a set of branches; exactly one is chosen per planning step using weights.
type Weighted struct {
	Branches []WeightedBranch
}

// WeightedBranch is one weighted arm after splitting on '^'.
type WeightedBranch struct {
	Weight int
	// IsFirst marks a [first] annotation (only meaningful when not on retry path and session allows).
	IsFirst bool
	// Target is the resolved primary for this branch.
	Target Primary
}

// String returns a stable diagnostic key for a primary (exclusion / health maps).
func (p Primary) String() string {
	var b strings.Builder
	if p.Backend != "" {
		b.WriteString(p.Backend)
		b.WriteByte(':')
	}
	b.WriteString(p.Model)
	if len(p.Params) > 0 {
		b.WriteByte('?')
		b.WriteString(p.Params.Encode())
	}
	return b.String()
}
