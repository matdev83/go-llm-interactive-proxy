package authority

import (
	"context"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// LeaseDecisionKind is the outcome of a concurrency lease admit.
type LeaseDecisionKind string

const (
	LeaseAllow    LeaseDecisionKind = "allow"
	LeaseDeny     LeaseDecisionKind = "deny"
	LeaseAdvisory LeaseDecisionKind = "advisory"
)

// IsKnown reports whether k is a documented lease decision kind.
func (k LeaseDecisionKind) IsKnown() bool {
	switch k {
	case LeaseAllow, LeaseDeny, LeaseAdvisory:
		return true
	}
	return false
}

// LeaseState is the queryable state of a concurrency lease.
type LeaseState string

const (
	LeaseStateActive   LeaseState = "active"
	LeaseStateExpiring LeaseState = "expiring"
	LeaseStateExpired  LeaseState = "expired"
	LeaseStateReleased LeaseState = "released"
)

// LeaseAdmission requests a logical-request concurrency lease (Phase 8 implements).
type LeaseAdmission struct {
	RequestID      string                      `json:"request_id"`
	Scope          scope.PrincipalScopeView    `json:"scope"`
	RuleID         string                      `json:"rule_id,omitempty"`
	Namespace      string                      `json:"namespace,omitempty"`
	TTL            time.Duration               `json:"ttl,omitempty"`
	BoundVersion   economics.PolicySnapshotRef `json:"bound_version,omitempty"`
	IdempotencyKey string                      `json:"idempotency_key,omitempty"`
	// Lifecycle identifies auxiliary vs top-level logical request (requirement 10.10).
	Lifecycle LifecycleScope `json:"lifecycle,omitempty"`
	// ParentLeaseID is the parent occupancy when Lifecycle is auxiliary and policy inherits.
	ParentLeaseID string `json:"parent_lease_id,omitempty"`
	// AuxPolicy controls whether auxiliary calls inherit the parent lease ("", "inherit", "acquire_own").
	AuxPolicy string `json:"aux_policy,omitempty"`
}

// LeaseDecision is the admit result for a concurrency lease.
type LeaseDecision struct {
	Kind           LeaseDecisionKind           `json:"kind"`
	LeaseID        string                      `json:"lease_id,omitempty"`
	Generation     int64                       `json:"generation,omitempty"`
	ExpiresAt      time.Time                   `json:"expires_at,omitempty"`
	RemainingSlots int                         `json:"remaining_slots,omitempty"`
	Readiness      Readiness                   `json:"readiness,omitempty"`
	BoundVersion   economics.PolicySnapshotRef `json:"bound_version,omitempty"`
	Evidence       SafeEvidence                `json:"evidence,omitempty"`
	// RenewBefore is the configured offset before ExpiresAt when heartbeat should renew.
	RenewBefore time.Duration `json:"renew_before,omitempty"`
	// TTL is the lease lifetime used for renew extensions.
	TTL time.Duration `json:"ttl,omitempty"`
	// FailureBehavior is the post-admission renew-failure posture (fail_closed|fail_open).
	FailureBehavior FailureBehavior `json:"failure_behavior,omitempty"`
}

// Validate checks LeaseDecisionKind when set.
func (d LeaseDecision) Validate() error {
	if d.Kind != "" && !d.Kind.IsKnown() {
		return fmt.Errorf("authority: unknown lease decision kind %q", d.Kind)
	}
	return nil
}

// LeaseRelease releases a previously acquired lease.
type LeaseRelease struct {
	LeaseID   string `json:"lease_id"`
	RequestID string `json:"request_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// LeaseRenew extends an active lease before expiry using generation CAS.
// ExpectedGeneration must match the lease's current generation; a mismatch or
// non-active lease must not resurrect released/expired capacity.
type LeaseRenew struct {
	LeaseID            string        `json:"lease_id"`
	RequestID          string        `json:"request_id,omitempty"`
	ExpectedGeneration int64         `json:"expected_generation"`
	TTL                time.Duration `json:"ttl,omitempty"`
}

// LeaseQuery is a bounded filter for active/history lease queries.
type LeaseQuery struct {
	RequestID string                   `json:"request_id,omitempty"`
	LeaseID   string                   `json:"lease_id,omitempty"`
	RuleID    string                   `json:"rule_id,omitempty"`
	Scope     scope.PrincipalScopeView `json:"scope,omitempty"`
	State     LeaseState               `json:"state,omitempty"`
	Limit     int                      `json:"limit,omitempty"`
	Cursor    string                   `json:"cursor,omitempty"`
}

// LeaseRecord is one lease row returned by query (no store types).
type LeaseRecord struct {
	LeaseID      string                      `json:"lease_id"`
	RequestID    string                      `json:"request_id,omitempty"`
	State        LeaseState                  `json:"state"`
	Generation   int64                       `json:"generation,omitempty"`
	ExpiresAt    time.Time                   `json:"expires_at,omitempty"`
	ReleasedAt   time.Time                   `json:"released_at,omitempty"`
	RuleID       string                      `json:"rule_id,omitempty"`
	Version      economics.PolicySnapshotRef `json:"version,omitempty"`
	DimensionKey string                      `json:"dimension_key,omitempty"` // safe scope correlation
}

// LeasePage is a bounded page of lease records.
type LeasePage struct {
	Leases     []LeaseRecord `json:"leases"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// ConcurrencyProvider is the Phase 8 seam for logical-request leases.
// This package defines the contract only; no store implementation lives here.
type ConcurrencyProvider interface {
	AdmitLease(ctx context.Context, in LeaseAdmission) (LeaseDecision, error)
	RenewLease(ctx context.Context, in LeaseRenew) (LeaseDecision, error)
	ReleaseLease(ctx context.Context, in LeaseRelease) error
	QueryLeases(ctx context.Context, q LeaseQuery) (LeasePage, error)
}
