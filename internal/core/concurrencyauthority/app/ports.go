package app

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Clock reports the current time for deterministic orchestration and tests.
type Clock interface {
	Now() time.Time
}

// RuleSource provides immutable concurrency rule snapshots.
type RuleSource interface {
	Snapshot(ctx context.Context) (RuleSnapshot, error)
}

// LeaseStore is the persistence port implemented by task 8.2 dialects.
type LeaseStore interface {
	Acquire(ctx context.Context, cmd AcquireCommand) (AcquireResult, error)
	Renew(ctx context.Context, cmd RenewCommand) (RenewResult, error)
	Release(ctx context.Context, cmd ReleaseCommand) (ReleaseResult, error)
	Query(ctx context.Context, q QueryCommand) (QueryResult, error)
	CheckReadiness(ctx context.Context) (domain.Readiness, error)
}

// RuleSnapshot is the immutable rule set consumed by admit orchestration.
type RuleSnapshot struct {
	Readiness domain.Readiness
	Rules     []domain.Rule
	FetchedAt time.Time
}

// AcquireCommand requests an idempotent lease insert under a capacity limit.
type AcquireCommand struct {
	Lease      domain.Lease
	RuleID     string
	Dimensions domain.Dimensions
	Limit      int
	Mode       domain.RuleMode
	Now        time.Time
}

// AcquireResult is the store outcome for one acquire attempt.
type AcquireResult struct {
	Lease            domain.Lease
	Replayed         bool
	RemainingSlots   int
	CapacityExceeded bool
	Rejected         bool
}

// RenewCommand extends a lease with generation CAS.
type RenewCommand struct {
	LeaseID            string
	RequestID          string
	ExpectedGeneration int64
	TTL                time.Duration
	Now                time.Time
}

// RenewResult is the store outcome for renew.
type RenewResult struct {
	Lease domain.Lease
}

// ReleaseCommand releases a lease idempotently.
type ReleaseCommand struct {
	LeaseID   string
	RequestID string
	Reason    string
	Now       time.Time
}

// ReleaseResult is the store outcome for release.
type ReleaseResult struct {
	Applied bool
	Lease   domain.Lease
}

// QueryCommand is a bounded lease query.
type QueryCommand struct {
	LeaseID   string
	RequestID string
	State     domain.LeaseState
	Now       time.Time
	Limit     int
}

// QueryResult is a bounded page of leases.
type QueryResult struct {
	Leases []domain.Lease
}

// AdmitInput is application-level lease admission input.
type AdmitInput struct {
	RequestID      string
	Scope          scope.PrincipalScopeView
	Namespace      string
	TTL            time.Duration
	BoundVersion   economics.PolicySnapshotRef
	IdempotencyKey string
	Lifecycle      metering.LifecycleScope
	ParentLeaseID  string
	AuxPolicy      domain.AuxPolicy
	RuleID         string
}

// AdmitResult is the application admit outcome.
type AdmitResult struct {
	Kind           domain.DecisionKind
	LeaseID        string
	Generation     int64
	ExpiresAt      time.Time
	RemainingSlots int
	Readiness      domain.Readiness
	BoundVersion   economics.PolicySnapshotRef
	Evidence       domain.Evidence
	Acquired       bool
	Replayed       bool
	RuleID         string
	RenewBefore    time.Duration
	TTL            time.Duration
}

// RenewInput is application-level lease renewal input.
type RenewInput struct {
	LeaseID            string
	RequestID          string
	ExpectedGeneration int64
	TTL                time.Duration
}

// ReleaseInput is application-level lease release input.
type ReleaseInput struct {
	LeaseID   string
	RequestID string
	Reason    string
}
