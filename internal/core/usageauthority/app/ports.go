package app

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Clock reports the current time for deterministic orchestration and tests.
// A nil Clock means "use system wall-clock time" (see Service.now).
type Clock interface {
	Now() time.Time
}

// RuleSource provides immutable rule snapshots to the app layer.
type RuleSource interface {
	Snapshot(ctx context.Context) (RuleSnapshot, error)
}

// StateStore provides atomic reservation, settlement, release, and bounded
// query operations without leaking storage details into the app layer.
type StateStore interface {
	Reserve(ctx context.Context, cmd ReserveCommand) (ReserveResult, error)
	Settle(ctx context.Context, cmd SettleCommand) (SettleResult, error)
	Release(ctx context.Context, cmd ReleaseCommand) (ReleaseResult, error)
	LimitStatus(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error)
	DecisionHistory(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error)
	CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error)
}

// EvidenceSink projects app outcomes into policydecision and control-plane
// evidence without exposing storage or HTTP concerns.
type EvidenceSink interface {
	RecordPolicyDecision(ctx context.Context, record policydecision.Record) error
	RecordAccountingAuthority(ctx context.Context, event controlplane.Event) error
}

// RuleSnapshot is the immutable rule set consumed by admission orchestration.
type RuleSnapshot struct {
	Status             domain.AuthorityStatus
	UnknownAttribution domain.UnknownAttribution
	Rules              []domain.Rule
	FetchedAt          time.Time
}

// ReserveCommand, SettleCommand, and ReleaseCommand are the app-owned mutation
// ports passed to the backing authority store.
type ReserveCommand struct {
	ReservationKey domain.ReservationKey
	RuleID         string
	RuleType       string
	Dimensions     domain.Dimensions
	Request        domain.Amount
	Spend          domain.Amount
	Authority      domain.AuthorityLevel
	EstimateOnly   bool
	At             time.Time
	SourceKey      string
}

type SettleCommand struct {
	SettlementKey  domain.SettlementKey
	ReservationKey domain.ReservationKey
	ReservationID  string
	RuleID         string
	Kind           SettlementKind
	FinalUsage     domain.Amount
	FinalCost      domain.Amount
	ReservedUsage  domain.Amount
	EstimatedUsage domain.Amount
	EstimatedCost  domain.Amount
	Authority      domain.AuthorityLevel
	ClientCanceled bool
	At             time.Time
	SourceKey      string
}

type ReleaseCommand struct {
	ReleaseKey     domain.ReleaseKey
	ReservationKey domain.ReservationKey
	ReservationID  string
	RuleID         string
	Kind           ReleaseKind
	Amount         domain.Amount
	At             time.Time
	SourceKey      string
}

type ReserveResult struct {
	Applied        bool
	ReservationID  string
	ReservedAmount domain.Amount
	RuleID         string
	RuleType       string
}

// AdmissionInput carries the safe request data required for pre-backend
// accounting admission. Cost and usage estimates are computed by the runtime
// driving adapter and supplied here; the app does not pull them through ports.
type AdmissionInput struct {
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	Dimensions     domain.Dimensions
	Request        domain.Amount
	RequestCount   domain.Amount
	PreflightUsage domain.PreflightUsage
	Spend          domain.Amount
	Authority      domain.AuthorityLevel
	ReservationKey domain.ReservationKey
	EstimateOnly   bool
}

// AdmissionResult reports the legal pre-backend accounting decision.
type AdmissionResult struct {
	Allowed         bool
	Outcome         domain.DecisionOutcome
	RuleIDs         []string
	RuleKind        domain.RuleKind
	ReservationID   string
	Reserved        bool
	ReservedAmount  domain.Amount
	PolicyRecord    policydecision.Record
	AccountingEvent controlplane.Event
}

// SettlementKind classifies the settlement path.
type SettlementKind string

const (
	SettlementKindFinal        SettlementKind = "final"
	SettlementKindPartial      SettlementKind = "partial"
	SettlementKindUnavailable  SettlementKind = "unavailable"
	SettlementKindCancellation SettlementKind = "cancellation"
	SettlementKindSwallowed    SettlementKind = "swallowed"
	SettlementKindLosing       SettlementKind = "losing"
)

// SettleInput carries the safe request data required for surfaced-attempt
// settlement orchestration.
type SettleInput struct {
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	ReservationKey domain.ReservationKey
	ReservationID  string
	RuleID         string
	Kind           SettlementKind
	FinalUsage     domain.Amount
	FinalCost      domain.Amount
	ReservedUsage  domain.Amount
	EstimatedUsage domain.Amount
	EstimatedCost  domain.Amount
	Authority      domain.AuthorityLevel
	ClientCanceled bool
}

// SettleResult reports the settlement outcome for surfaced attempts.
type SettleResult struct {
	Applied         bool
	ReservationID   string
	ReleasedDelta   domain.Amount
	OverageDelta    domain.Amount
	AdjustmentDelta domain.Amount
	PolicyRecord    policydecision.Record
	AccountingEvent controlplane.Event
}

// ReleaseKind classifies the release path for swallowed or losing attempts.
type ReleaseKind string

const (
	ReleaseKindSwallowed ReleaseKind = "swallowed"
	ReleaseKindLosing    ReleaseKind = "losing"
)

// ReleaseInput carries the safe request data required to release a reservation
// without attributing surfaced usage to the released attempt.
type ReleaseInput struct {
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	ReservationKey domain.ReservationKey
	ReservationID  string
	RuleID         string
	Kind           ReleaseKind
	Amount         domain.Amount
}

// ReleaseResult reports the release outcome for swallowed or losing attempts.
type ReleaseResult struct {
	Applied         bool
	ReservationID   string
	ReleasedDelta   domain.Amount
	PolicyRecord    policydecision.Record
	AccountingEvent controlplane.Event
}

// QueryState classifies the bounded live authority query posture.
type QueryState string

const (
	QueryStateDisabled     QueryState = "disabled"
	QueryStateEmpty        QueryState = "empty"
	QueryStateUnsupported  QueryState = "unsupported"
	QueryStateTooBroad     QueryState = "too_broad"
	QueryStateAdvisoryOnly QueryState = "advisory_only"
	QueryStateDegraded     QueryState = "degraded"
	QueryStateUnavailable  QueryState = "unavailable"
	QueryStateReady        QueryState = "ready"
)

// LimitStatusResult and DecisionHistoryResult are bounded query DTOs that keep
// live authority state distinct from historical rows.
type LimitStatusResult struct {
	State QueryState
	Page  controlplane.Page[controlplane.AccountingLimitStatusRow]
}

type DecisionHistoryResult struct {
	State QueryState
	Page  controlplane.Page[controlplane.AccountingDecisionRow]
}

// Evidence is the slim, varying subset of fields that an admission,
// settlement, or release call site contributes to the policydecision and
// control-plane projectors. Authority, WindowStart/End/ResetAt, Remaining,
// and the non-varying source identity, visibility, evidence/redaction state
// are derived by the projector from (status, reserved, in).
type Evidence struct {
	At              time.Time
	Correlation     controlplane.Correlation
	Scope           scope.PrincipalScopeView
	RuleID          string
	RuleType        string
	Outcome         controlplane.AccountingOutcome
	ReasonCode      policydecision.AccountingReasonCode
	ReservationID   string
	SettlementState controlplane.AccountingSettlementState
	Unit            string
	Currency        string
	Limit           int64
	Consumed        int64
	Reserved        int64
	Adjustment      int64
}
