package domain

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type RuleKind string

const (
	RuleKindQuota RuleKind = "quota"
	RuleKindRate  RuleKind = "rate"
)

func (k RuleKind) IsKnown() bool {
	switch k {
	case RuleKindQuota, RuleKindRate:
		return true
	default:
		return false
	}
}

type RuleMode string

const (
	RuleModeStrict   RuleMode = "strict"
	RuleModeAdvisory RuleMode = "advisory"
)

func (m RuleMode) IsKnown() bool {
	return m == "" || m == RuleModeStrict || m == RuleModeAdvisory
}

type AuthorityRequirement string

const (
	AuthorityRequirementAny           AuthorityRequirement = ""
	AuthorityRequirementEstimated     AuthorityRequirement = "estimated"
	AuthorityRequirementAuthoritative AuthorityRequirement = "authoritative"
)

func (r AuthorityRequirement) IsKnown() bool {
	switch r {
	case AuthorityRequirementAny, AuthorityRequirementEstimated, AuthorityRequirementAuthoritative:
		return true
	default:
		return false
	}
}

type FailureBehavior string

const (
	FailureBehaviorDefault    FailureBehavior = ""
	FailureBehaviorFailClosed FailureBehavior = "fail_closed"
	FailureBehaviorFailOpen   FailureBehavior = "fail_open"
)

func (b FailureBehavior) IsKnown() bool {
	switch b {
	case FailureBehaviorDefault, FailureBehaviorFailClosed, FailureBehaviorFailOpen:
		return true
	default:
		return false
	}
}

type Rule struct {
	ID                   string
	Kind                 RuleKind
	Mode                 RuleMode
	Unit                 AmountUnit
	Limit                Amount
	AuthorityRequirement AuthorityRequirement
	FailureBehavior      FailureBehavior
	Window               WindowSpec
	Match                DimensionsMatcher
	// Dual-plane metadata (Phase 7). Legacy rules must set Basis to
	// BasisLegacyProviderPreferredAttempt; new rules require perspective,
	// lifecycle_scope, basis, and namespace.
	Perspective    metering.EconomicPerspective
	LifecycleScope metering.LifecycleScope
	Basis          MeteringBasis
	Namespace      string
	Version        string
}

func (r Rule) Matches(actual Dimensions) bool {
	return r.Match.Matches(actual)
}
