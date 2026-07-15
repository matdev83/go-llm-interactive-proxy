package app

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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

// StateStore provides atomic reservation, settlement, release, advisory usage
// application, and bounded query operations without leaking storage details
// into the app layer.
type StateStore interface {
	Reserve(ctx context.Context, cmd ReserveCommand) (ReserveResult, error)
	Settle(ctx context.Context, cmd SettleCommand) (SettleResult, error)
	Release(ctx context.Context, cmd ReleaseCommand) (ReleaseResult, error)
	ApplyUsage(ctx context.Context, cmd ApplyUsageCommand) (ApplyUsageResult, error)
	ActiveLimit(ctx context.Context, q ActiveLimitQuery) (controlplane.AccountingLimitStatusRow, bool, error)
	LimitStatus(ctx context.Context, q controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error)
	DecisionHistory(ctx context.Context, q controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error)
	CheckReadiness(ctx context.Context) (domain.AuthorityStatus, error)
}

// ActiveLimitQuery resolves one configured rule's current live row using the
// same normalized dimensions and window time as reservation matching.
type ActiveLimitQuery struct {
	RuleID     string
	Dimensions domain.Dimensions
	At         time.Time
}

// EvidenceSink projects app outcomes into policydecision and control-plane
// evidence without exposing storage or HTTP concerns.
type EvidenceSink interface {
	RecordPolicyDecision(ctx context.Context, record policydecision.Record) error
	RecordAccountingAuthority(ctx context.Context, event controlplane.Event) error
}

// RuleSnapshot is the immutable rule set consumed by admission orchestration.
type RuleSnapshot struct {
	ID                 string
	Version            string
	EffectiveAt        time.Time
	FetchedAt          time.Time
	State              economics.SnapshotState
	Status             domain.AuthorityStatus
	UnknownAttribution domain.UnknownAttribution
	Rules              []domain.Rule
}

// PolicyRef returns the bindable policy snapshot identity for this rule set.
func (s RuleSnapshot) PolicyRef() economics.PolicySnapshotRef {
	id := s.ID
	if id == "" {
		id = "usage_authority"
	}
	return economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{
			ID:          id,
			Version:     s.Version,
			EffectiveAt: s.EffectiveAt,
			FetchedAt:   s.FetchedAt,
		},
		PolicyID: id,
	}
}

// SnapshotStateFromAuthority maps usage-authority status onto economics.SnapshotState.
func SnapshotStateFromAuthority(st domain.AuthorityStatus) economics.SnapshotState {
	switch st.State {
	case domain.AuthorityStateReady:
		return economics.SnapshotReady
	case domain.AuthorityStateDegraded, domain.AuthorityStateAdvisoryOnly:
		return economics.SnapshotDegraded
	case domain.AuthorityStateUnavailable:
		return economics.SnapshotUnavailable
	case domain.AuthorityStateDisabled:
		return economics.SnapshotDisabled
	default:
		if st.State == "" {
			return economics.SnapshotReady
		}
		return economics.SnapshotUnavailable
	}
}

// ReservationDescriptor is the app-owned, per-rule mutation descriptor. A
// reservation set is passed to the store as one logical operation so every
// matched strict rule observes the same atomic commit boundary.
type ReservationDescriptor struct {
	RuleID         string
	Kind           domain.RuleKind
	Unit           domain.AmountUnit
	Currency       string
	Dimensions     domain.Dimensions
	ReservationKey domain.ReservationKey
	ReservationID  string
	Amount         domain.Amount
	SourceKey      string
	Authority      domain.AuthorityLevel
}

// ReservationSet is the complete set of strict rule reservations for one
// logical request/B-leg mutation.
type ReservationSet []ReservationDescriptor

// SettlementDescriptor carries one rule's reserved and final enforceable
// amounts. The store applies all descriptors in one atomic operation.
type SettlementDescriptor struct {
	Reservation    ReservationDescriptor
	FinalUsage     domain.Amount
	FinalCost      domain.Amount
	EstimatedUsage domain.Amount
	EstimatedCost  domain.Amount
	SourceKey      string
	Sequence       int
	Authority      domain.AuthorityLevel
	// MeasurementAuthority keeps token/request authority independent from
	// monetary-cost authority. Authority is retained for compatibility with
	// older adapters and mirrors the usage authority when present.
	MeasurementAuthority MeasurementAuthority
}

// MeasurementAuthority describes authority separately for token/request usage
// and monetary cost. AuthoritativeCostPresent distinguishes a provider's
// authoritative zero cost from an absent cost value.
type MeasurementAuthority struct {
	Usage                    domain.AuthorityLevel
	Cost                     domain.AuthorityLevel
	AuthoritativeCostPresent bool
}

func (a MeasurementAuthority) usage() domain.AuthorityLevel {
	if a.Usage != "" {
		return a.Usage
	}
	return domain.AuthorityLevelAny
}

func (a MeasurementAuthority) cost() domain.AuthorityLevel {
	if a.Cost != "" {
		return a.Cost
	}
	return domain.AuthorityLevelAny
}

// ForUnit returns the authority relevant to the supplied enforceable unit.
func (a MeasurementAuthority) ForUnit(unit domain.AmountUnit) domain.AuthorityLevel {
	if unit == domain.AmountUnitMoneyNano {
		return a.cost()
	}
	return a.usage()
}

// ReleaseDescriptor carries one rule's reservation to release. The store
// applies all descriptors in one atomic operation.
type ReleaseDescriptor struct {
	Reservation ReservationDescriptor
	SourceKey   string
	Sequence    int
}

// ReserveCommand, SettleCommand, and ReleaseCommand are the app-owned mutation
// ports passed to the backing authority store.
type ReserveCommand struct {
	Reservations   ReservationSet
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
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
	Reservations         []SettlementDescriptor
	Correlation          controlplane.Correlation
	Scope                scope.PrincipalScopeView
	SettlementKey        domain.SettlementKey
	ReservationKey       domain.ReservationKey
	ReservationID        string
	RuleID               string
	Kind                 SettlementKind
	FinalUsage           domain.Amount
	FinalCost            domain.Amount
	ReservedUsage        domain.Amount
	EstimatedUsage       domain.Amount
	EstimatedCost        domain.Amount
	Authority            domain.AuthorityLevel
	MeasurementAuthority MeasurementAuthority
	FinalUsagePresent    bool
	FinalCostPresent     bool
	Stage                string
	BackendAttempted     bool
	OutputCommitted      bool
	ClientCanceled       bool
	At                   time.Time
	SourceKey            string
}

type ReleaseCommand struct {
	Reservations     []ReleaseDescriptor
	Correlation      controlplane.Correlation
	Scope            scope.PrincipalScopeView
	ReleaseKey       domain.ReleaseKey
	ReservationKey   domain.ReservationKey
	ReservationID    string
	RuleID           string
	Kind             ReleaseKind
	Amount           domain.Amount
	Authority        domain.AuthorityLevel
	Stage            string
	BackendAttempted bool
	OutputCommitted  bool
	At               time.Time
	SourceKey        string
}

type ReserveResult struct {
	Applied        bool
	ReservationID  string
	ReservedAmount domain.Amount
	RuleID         string
	RuleType       string
	Reservations   []AdmissionReservation
}

// AdmissionReservation reports one applied reservation created by Admit.
// The legacy single-reservation fields on AdmissionResult remain the primary
// reservation for compatibility with older callers.
type AdmissionReservation struct {
	ReservationID  string
	RuleID         string
	ReservedAmount domain.Amount
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
	// Phase 7.2: lifecycle stage for rule filtering; exposure/facts for amount selection.
	LifecycleScope metering.LifecycleScope
	Perspective    metering.EconomicPerspective
	Exposure       economics.ExposureBasis
	Facts          []metering.Fact
}

// AdmissionClamp reports a spend-cap clamp (requirement 6.5): the request's
// estimated spend exceeded a strict spend cap, so admission reduced the
// reserved exposure to the remaining budget. RequestedMax is the original
// spend basis from the domain evaluation; EffectiveMax is the reduced
// exposure (money nano) the app reserved. FailureBehavior is the effective
// posture the runtime must apply when it cannot convert EffectiveMax to a
// token count (cost-unavailable, requirement 5.5).
type AdmissionClamp struct {
	RuleID          string
	RequestedMax    domain.Amount
	EffectiveMax    domain.Amount
	FailureBehavior domain.FailureBehavior
	Reason          string
}

// AdmissionResult reports the legal pre-backend accounting decision.
type AdmissionResult struct {
	Allowed bool
	Outcome domain.DecisionOutcome
	RuleIDs []string
	// AdvisoryRuleIDs are the matched rules configured with RuleModeAdvisory.
	// They are tracked separately from RuleIDs so the runtime can apply final
	// advisory usage to advisory windows even when no strict reservation was
	// created (requirement 7.7). Advisory rules never reserve.
	AdvisoryRuleIDs []string
	// UnreservedRuleIDs are matched rules whose allowed execution created no
	// reservation (advisory and strict fail-open paths). Their final usage is
	// applied through ApplyUsage at lifecycle completion.
	UnreservedRuleIDs []string
	// SelectedRuleID identifies the rule whose outcome won admission severity.
	// RuleIDs remains the complete matched set for multi-rule evidence.
	SelectedRuleID  string
	RuleKind        domain.RuleKind
	ReservationID   string
	Reserved        bool
	ReservedAmount  domain.Amount
	Reservations    []AdmissionReservation
	Clamp           *AdmissionClamp
	PolicyRecord    policydecision.Record
	AccountingEvent controlplane.Event
	// BoundVersion is the policy snapshot identity captured at admission (11.2).
	BoundVersion economics.PolicySnapshotRef
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
	Reservations   []SettlementDescriptor
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	ReservationKey domain.ReservationKey
	ReservationID  string
	RuleID         string
	Kind           SettlementKind
	// Sequence distinguishes partial, cancellation, final, and authoritative
	// reconciliation mutations that share one reservation identity.
	Sequence             int
	FinalUsage           domain.Amount
	FinalCost            domain.Amount
	ReservedUsage        domain.Amount
	EstimatedUsage       domain.Amount
	EstimatedCost        domain.Amount
	Authority            domain.AuthorityLevel
	MeasurementAuthority MeasurementAuthority
	FinalUsagePresent    bool
	FinalCostPresent     bool
	Stage                string
	BackendAttempted     bool
	OutputCommitted      bool
	ClientCanceled       bool
	// Phase 7.2 settlement selection: dual-plane rules resolve amounts from
	// Facts/Exposure; compatibility-basis rules keep FinalUsage/FinalCost.
	Exposure economics.ExposureBasis
	Facts    []metering.Fact
	// BoundVersion pins settlement to the admission-time policy snapshot (11.4).
	BoundVersion economics.PolicySnapshotRef
}

// SettleResult reports the settlement outcome for surfaced attempts.
type SettleResult struct {
	Applied          bool
	ReservationID    string
	ReleasedDelta    domain.Amount
	OverageDelta     domain.Amount
	AdjustmentDelta  domain.Amount
	Mutations        []SettlementMutation
	PolicyRecords    []policydecision.Record
	AccountingEvents []controlplane.Event
	PolicyRecord     policydecision.Record
	AccountingEvent  controlplane.Event
}

// SettlementMutation contains the per-rule result of one atomic settlement
// set. The first mutation is mirrored into the legacy aggregate fields on
// SettleResult for callers that only enforce one rule.
type SettlementMutation struct {
	RuleID          string
	ReservationID   string
	ReleasedDelta   domain.Amount
	OverageDelta    domain.Amount
	AdjustmentDelta domain.Amount
}

// ReleaseKind classifies the release path for swallowed or losing attempts.
type ReleaseKind string

const (
	ReleaseKindSwallowed        ReleaseKind = "swallowed"
	ReleaseKindLosing           ReleaseKind = "losing"
	ReleaseKindAdmissionFailure ReleaseKind = "admission_failure"
)

// SettlementSequence returns the stable replay sequence for one lifecycle
// stage. Authoritative final reconciliation is deliberately distinct from an
// estimated final settlement even though both use SettlementKindFinal.
func SettlementSequence(kind SettlementKind, authority domain.AuthorityLevel) int {
	switch kind {
	case SettlementKindPartial:
		return 1
	case SettlementKindCancellation:
		return 2
	case SettlementKindUnavailable:
		return 3
	case SettlementKindFinal:
		if authority == domain.AuthorityLevelAuthoritative {
			return 5
		}
		return 4
	case SettlementKindSwallowed:
		return 6
	case SettlementKindLosing:
		return 7
	default:
		return 1
	}
}

// ReleaseSequence preserves the reservation-level release idempotency key.
// Releases are terminal for a reservation, so distinct release labels must
// still converge on the same replay sequence.
func ReleaseSequence(kind ReleaseKind) int {
	return 1
}

// ReleaseInput carries the safe request data required to release a reservation
// without attributing surfaced usage to the released attempt.
type ReleaseInput struct {
	Reservations   []ReleaseDescriptor
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	ReservationKey domain.ReservationKey
	ReservationID  string
	RuleID         string
	Kind           ReleaseKind
	// Sequence distinguishes release stages that share one reservation identity.
	Sequence         int
	Amount           domain.Amount
	Authority        domain.AuthorityLevel
	Stage            string
	BackendAttempted bool
	OutputCommitted  bool
}

// ReleaseResult reports the release outcome for swallowed or losing attempts.
type ReleaseResult struct {
	Applied          bool
	ReservationID    string
	ReleasedDelta    domain.Amount
	Mutations        []ReleaseMutation
	PolicyRecords    []policydecision.Record
	AccountingEvents []controlplane.Event
	PolicyRecord     policydecision.Record
	AccountingEvent  controlplane.Event
}

// ReleaseMutation contains the per-rule result of one atomic release set.
type ReleaseMutation struct {
	RuleID        string
	ReservationID string
	ReleasedDelta domain.Amount
}

// ApplyUsageCommand applies final usage/cost to matched accounting windows
// WITHOUT requiring a reservation (requirement 7.7). It is used for advisory
// rules and for any request that produced usage but never created a strict
// reservation. RuleIDs are the matched rules whose windows should accumulate
// the usage; Usage and RequestCount carry the final per-unit token/request
// breakdown so the store can select the right amount per rule unit, and
// FinalCost carries the final cost for money (budget/spend-cap) rules. No
// reservation record is created. Idempotent via SourceKey.
type ApplyUsageCommand struct {
	Correlation          controlplane.Correlation
	Scope                scope.PrincipalScopeView
	Dimensions           domain.Dimensions
	RuleIDs              []string
	Usage                domain.PreflightUsage
	RequestCount         domain.Amount
	FinalCost            domain.Amount
	Authority            domain.AuthorityLevel
	MeasurementAuthority MeasurementAuthority
	Kind                 SettlementKind
	UsagePresent         bool
	CostPresent          bool
	At                   time.Time
	SourceKey            string
}

// ApplyUsageResult reports which advisory windows were updated. Applied is true
// when at least one matched rule's window consumed usage. RuleIDs lists the
// rules that were actually updated (rules with no matching live window are
// skipped).
type ApplyUsageResult struct {
	Applied bool
	RuleIDs []string
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
	MatchedRuleIDs  []string
	RuleType        string
	RequestedMax    domain.Amount
	EffectiveMax    domain.Amount
	ClampReason     string
	Outcome         controlplane.AccountingOutcome
	ReasonCode      policydecision.AccountingReasonCode
	ReservationID   string
	SettlementState controlplane.AccountingSettlementState
	// SourceKind and SourceSequence are included in event identity for mutation
	// stages. Admission evidence leaves them empty and zero.
	SourceKind       string
	SourceSequence   int
	Unit             string
	Currency         string
	Limit            int64
	Consumed         int64
	Reserved         int64
	Adjustment       int64
	Authority        domain.AuthorityLevel
	Stage            string
	BackendAttempted bool
	OutputCommitted  bool
}
