package controlplane

import (
	"strings"
	"time"
)

// EnforcementScope classifies whether a component provides distributed strict
// enforcement or advisory single-process posture (15.8).
type EnforcementScope string

const (
	EnforcementScopeDistributedStrict     EnforcementScope = "distributed_strict"
	EnforcementScopeAdvisorySingleProcess EnforcementScope = "advisory_single_process"
	EnforcementScopeDisabled              EnforcementScope = "disabled"
)

// IsKnown reports whether s is a documented enforcement scope.
func (s EnforcementScope) IsKnown() bool {
	switch s {
	case EnforcementScopeDistributedStrict, EnforcementScopeAdvisorySingleProcess, EnforcementScopeDisabled:
		return true
	}
	return false
}

// EnforcementScopeForStoreBacking maps configured store backing to enforcement
// scope. Local memory (and sqlite single-node stores) are never distributed
// strict (15.8, 16.7).
func EnforcementScopeForStoreBacking(store string, strictCapable bool) EnforcementScope {
	switch stringsTrimLower(store) {
	case "", "disabled":
		return EnforcementScopeDisabled
	case "memory", "sqlite":
		return EnforcementScopeAdvisorySingleProcess
	case "postgres":
		if strictCapable {
			return EnforcementScopeDistributedStrict
		}
		return EnforcementScopeAdvisorySingleProcess
	case "injected":
		if strictCapable {
			return EnforcementScopeDistributedStrict
		}
		return EnforcementScopeAdvisorySingleProcess
	default:
		return EnforcementScopeAdvisorySingleProcess
	}
}

// ReadinessComponentID identifies an independently reported readiness/journal
// source (15.7).
type ReadinessComponentID string

const (
	ReadinessComponentMeteringJournal       ReadinessComponentID = "metering_journal"
	ReadinessComponentControlPlane          ReadinessComponentID = "control_plane"
	ReadinessComponentUsageAuthority        ReadinessComponentID = "usage_authority"
	ReadinessComponentConcurrencyAuthority  ReadinessComponentID = "concurrency_authority"
	ReadinessComponentRequestCoordinator    ReadinessComponentID = "request_coordinator"
	ReadinessComponentAttemptCoordinator    ReadinessComponentID = "attempt_coordinator"
	ReadinessComponentUsageSnapshot         ReadinessComponentID = "usage_snapshot"
	ReadinessComponentConcurrencySnapshot   ReadinessComponentID = "concurrency_snapshot"
	ReadinessComponentRatingSnapshot        ReadinessComponentID = "rating_snapshot"
	ReadinessComponentCustomerRater         ReadinessComponentID = "customer_rater"
	ReadinessComponentOperatorRater         ReadinessComponentID = "operator_rater"
	ReadinessComponentSecretGuardQuarantine ReadinessComponentID = "secret_guard_quarantine"
)

// ReadinessComponentStatus is one independent authority/journal readiness row
// (15.7).
type ReadinessComponentStatus struct {
	Component        ReadinessComponentID `json:"component"`
	State            CapabilityState      `json:"state"`
	Reason           ReasonCode           `json:"reason,omitempty"`
	EnforcementScope EnforcementScope     `json:"enforcement_scope,omitempty"`
	StoreBacking     string               `json:"store_backing,omitempty"`
	LastUpdatedAt    time.Time            `json:"last_updated_at,omitzero"`
}

// ProtectedTrafficPosture aggregates whether required protected traffic may be
// served (15.7).
type ProtectedTrafficPosture struct {
	State          CapabilityState `json:"state"`
	Reason         ReasonCode      `json:"reason,omitempty"`
	MayServeStrict bool            `json:"may_serve_strict"`
	LastUpdatedAt  time.Time       `json:"last_updated_at,omitzero"`
}

// ReadinessReport is the full independent plus aggregate readiness snapshot
// (15.7).
type ReadinessReport struct {
	Components []ReadinessComponentStatus `json:"components"`
	Posture    ProtectedTrafficPosture    `json:"posture"`
}

// AggregateProtectedTrafficPosture derives aggregate posture from independent
// component rows (15.7).
func AggregateProtectedTrafficPosture(components []ReadinessComponentStatus, at time.Time) ProtectedTrafficPosture {
	worst := CapabilityReady
	var reason ReasonCode
	mayServeStrict := true
	for _, c := range components {
		if c.State == CapabilityDisabled {
			continue
		}
		if capabilityRank(c.State) > capabilityRank(worst) {
			worst = c.State
			reason = c.Reason
		}
		if c.EnforcementScope == EnforcementScopeDistributedStrict &&
			c.State != CapabilityReady && c.State != CapabilityDisabled {
			mayServeStrict = false
		}
	}
	if worst == CapabilityDisabled {
		worst = CapabilityReady
	}
	return ProtectedTrafficPosture{
		State:          worst,
		Reason:         reason,
		MayServeStrict: mayServeStrict,
		LastUpdatedAt:  at,
	}
}

func capabilityRank(s CapabilityState) int {
	switch s {
	case CapabilityReady:
		return 0
	case CapabilityDegraded:
		return 1
	case CapabilityUnavailable:
		return 2
	case CapabilityDisabled:
		return -1
	default:
		return 3
	}
}

func stringsTrimLower(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}
