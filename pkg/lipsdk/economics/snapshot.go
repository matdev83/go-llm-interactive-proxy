package economics

import (
	"context"
	"time"
)

// SnapshotState classifies versioned snapshot readiness (requirement 11.7).
type SnapshotState string

const (
	SnapshotReady       SnapshotState = "ready"
	SnapshotStale       SnapshotState = "stale"
	SnapshotDegraded    SnapshotState = "degraded"
	SnapshotUnavailable SnapshotState = "unavailable"
	SnapshotDisabled    SnapshotState = "disabled"
)

// IsKnown reports whether s is a documented snapshot state.
func (s SnapshotState) IsKnown() bool {
	switch s {
	case SnapshotReady, SnapshotStale, SnapshotDegraded, SnapshotUnavailable, SnapshotDisabled:
		return true
	default:
		return false
	}
}

// Snapshot is an immutable versioned envelope for policy or rating material
// (design: Versioned Snapshots / Dynamic Sources).
type Snapshot[T any] struct {
	ID          string        `json:"id"`
	Version     string        `json:"version"`
	EffectiveAt time.Time     `json:"effective_at,omitzero"`
	FetchedAt   time.Time     `json:"fetched_at,omitzero"`
	State       SnapshotState `json:"state"`
	Value       T             `json:"value"`
}

// Ref returns a PolicySnapshotRef for authority/concurrency binding.
func (s Snapshot[T]) PolicyRef(policyID string) PolicySnapshotRef {
	return PolicySnapshotRef{
		VersionRef: VersionRef{
			ID:          s.ID,
			Version:     s.Version,
			EffectiveAt: s.EffectiveAt,
			FetchedAt:   s.FetchedAt,
		},
		PolicyID: policyID,
	}
}

// RatingRef returns a RatingSnapshotRef for rating binding.
func (s Snapshot[T]) RatingRef(raterID string) RatingSnapshotRef {
	return RatingSnapshotRef{
		VersionRef: VersionRef{
			ID:          s.ID,
			Version:     s.Version,
			EffectiveAt: s.EffectiveAt,
			FetchedAt:   s.FetchedAt,
		},
		RaterID: raterID,
	}
}

// PolicyKind identifies which rule plane a policy snapshot represents.
type PolicyKind string

const (
	PolicyKindUsageAuthority PolicyKind = "usage_authority"
	PolicyKindConcurrency    PolicyKind = "concurrency"
)

// IsKnown reports whether k is a documented policy kind.
func (k PolicyKind) IsKnown() bool {
	switch k {
	case PolicyKindUsageAuthority, PolicyKindConcurrency:
		return true
	default:
		return false
	}
}

// PolicyRulesView is the public, opaque policy payload for injectable sources.
// Concrete rule decoding stays internal; enterprise modules may supply opaque bytes.
type PolicyRulesView struct {
	Kind    PolicyKind `json:"kind"`
	Payload []byte     `json:"payload,omitempty"`
}

// RatingCatalogView is the public rating/catalog snapshot payload.
type RatingCatalogView struct {
	Currency       string `json:"currency,omitempty"`
	CatalogVersion string `json:"catalog_version,omitempty"`
}

// RuleSnapshotSource provides immutable authority or concurrency rule snapshots
// (design: Dynamic Sources; requirements 11.1, 11.5, 11.6).
type RuleSnapshotSource interface {
	Snapshot(ctx context.Context) (Snapshot[PolicyRulesView], error)
}

// RatingSnapshotSource provides immutable rating/pricebook snapshots.
type RatingSnapshotSource interface {
	Snapshot(ctx context.Context) (Snapshot[RatingCatalogView], error)
}
