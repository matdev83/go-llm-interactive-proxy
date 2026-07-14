package domain

import (
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type AuthorityLevel string

const (
	AuthorityLevelAny           AuthorityLevel = ""
	AuthorityLevelEstimated     AuthorityLevel = "estimated"
	AuthorityLevelAuthoritative AuthorityLevel = "authoritative"
	AuthorityLevelUnavailable   AuthorityLevel = "unavailable"
)

func (a AuthorityLevel) IsKnown() bool {
	switch a {
	case AuthorityLevelAny, AuthorityLevelEstimated, AuthorityLevelAuthoritative, AuthorityLevelUnavailable:
		return true
	default:
		return false
	}
}

type EvaluationContext struct {
	Dimensions     Dimensions
	Amount         Amount
	Spend          Amount
	RequestCount   Amount
	PreflightUsage PreflightUsage
	Authority      AuthorityLevel
	At             time.Time
	// LifecycleScope filters rules for request vs attempt admission (Phase 7.2).
	// Empty disables filtering.
	LifecycleScope metering.LifecycleScope
	Exposure       economics.ExposureBasis
	Facts          []metering.Fact
}

type DecisionOutcome string

const (
	DecisionOutcomeAllow       DecisionOutcome = "allow"
	DecisionOutcomeAdvisory    DecisionOutcome = "advisory"
	DecisionOutcomeDeny        DecisionOutcome = "deny"
	DecisionOutcomeClamp       DecisionOutcome = "clamp"
	DecisionOutcomeUnavailable DecisionOutcome = "unavailable"
	DecisionOutcomeError       DecisionOutcome = "error"
)

func (o DecisionOutcome) severity() int {
	switch o {
	case DecisionOutcomeDeny:
		return 4
	case DecisionOutcomeClamp:
		return 3
	case DecisionOutcomeAdvisory:
		return 2
	case DecisionOutcomeUnavailable:
		return 1
	case DecisionOutcomeAllow:
		return 0
	case DecisionOutcomeError:
		return -1
	default:
		return -1
	}
}

type RuleMatch struct {
	RuleID   string
	RuleIDs  []string
	Kind     RuleKind
	Mode     RuleMode
	Exceeded bool
	Outcome  DecisionOutcome
	// RequestedMax carries the requested exposure basis for a clamp outcome
	// (e.g. the requested spend for a spend cap). The domain populates it
	// from the evaluation basis; EffectiveMax stays zero until the app fills
	// it from live store remaining plus the cost estimate (requirement 6.5).
	RequestedMax Amount
	// EffectiveMax is the reduced exposure after clamping. It is left zero
	// in the domain and populated by the app from live authority state.
	EffectiveMax Amount
}

type EvaluationResult struct {
	Matches  []RuleMatch
	Selected RuleMatch
}

// StrictUnavailableRuleIDs returns strict matches whose authority or accounting
// basis is unavailable. The app layer uses this set to resolve configured
// failure behavior before applying ordinary outcome severity.
func StrictUnavailableRuleIDs(matches []RuleMatch) []string {
	ids := make([]string, 0)
	for _, match := range matches {
		if match.Mode != RuleModeStrict || match.Outcome != DecisionOutcomeUnavailable {
			continue
		}
		if strings.TrimSpace(match.RuleID) != "" {
			ids = append(ids, match.RuleID)
		}
	}
	return ids
}

// SelectRuleOutcome selects the most restrictive outcome from matches while
// optionally excluding rules whose configured failure behavior is fail-open.
// Rule matching remains pure; application code decides which unavailable rules
// may be excluded after resolving their configured posture.
func SelectRuleOutcome(matches []RuleMatch, excluded map[string]struct{}) RuleMatch {
	result := RuleMatch{Outcome: DecisionOutcomeAllow}
	strictMatched := false
	advisoryUnavailable := false
	for _, match := range matches {
		if _, skip := excluded[match.RuleID]; skip {
			continue
		}
		if match.Mode == RuleModeStrict {
			strictMatched = true
		}
		if match.Mode == RuleModeAdvisory && match.Outcome == DecisionOutcomeUnavailable {
			advisoryUnavailable = true
			continue
		}
		if match.Outcome.severity() > result.Outcome.severity() {
			result = match
		}
	}
	if !strictMatched && advisoryUnavailable && result.Outcome == DecisionOutcomeAllow {
		result.Outcome = DecisionOutcomeAdvisory
	}
	return result
}

func EvaluateRules(rules []Rule, ctx EvaluationContext) EvaluationResult {
	result := EvaluationResult{Selected: RuleMatch{Outcome: DecisionOutcomeAllow}}
	matchedIDs := make([]string, 0, len(rules))
	for _, rule := range rules {
		if !rule.AppliesToLifecycle(ctx.LifecycleScope) {
			continue
		}
		// Dimension mismatch is the only reason a rule is excluded from
		// evidence. An authority mismatch no longer drops the rule: it
		// matches with an authority-unavailable outcome so the app can
		// resolve posture via configured failure behavior (requirement 8.3,
		// finding 5).
		if !rule.Matches(ctx.Dimensions) {
			continue
		}
		match := rule.evaluate(ctx)
		result.Matches = append(result.Matches, match)
		matchedIDs = append(matchedIDs, rule.ID)
	}

	if len(matchedIDs) > 0 {
		for i := range result.Matches {
			result.Matches[i].RuleIDs = append([]string(nil), matchedIDs...)
		}
		result.Selected = SelectRuleOutcome(result.Matches, nil)
		result.Selected.RuleIDs = append([]string(nil), matchedIDs...)
	}
	return result
}

func (r Rule) evaluate(ctx EvaluationContext) RuleMatch {
	match := RuleMatch{RuleID: r.ID, Kind: r.Kind, Mode: r.Mode}
	if !r.Matches(ctx.Dimensions) {
		return match
	}
	// An authoritative-only rule that matched on dimensions still appears
	// in evidence when only estimated/unavailable evidence exists; it
	// reports an authority-unavailable outcome. Reusing the existing
	// unavailable outcome keeps the severity ordering deny > clamp >
	// advisory > unavailable > allow, so it never overrides a real deny
	// but is visible and resolvable by the app (requirement 8.3).
	if !r.matchesAuthority(ctx.Authority) {
		match.Outcome = DecisionOutcomeUnavailable
		return match
	}

	unit := r.Unit
	if unit == "" {
		unit = r.Limit.Unit
	}

	basis, ok := r.SelectAmount(AmountSelectionSource{
		Amount:         ctx.Amount,
		Spend:          ctx.Spend,
		RequestCount:   ctx.RequestCount,
		PreflightUsage: ctx.PreflightUsage,
		Exposure:       ctx.Exposure,
		Facts:          ctx.Facts,
	})
	if !ok {
		match.Outcome = DecisionOutcomeUnavailable
		return match
	}
	if r.Kind == RuleKindBudget || r.Kind == RuleKindSpendCap {
		if strings.TrimSpace(basis.Currency) == "" {
			match.Outcome = DecisionOutcomeUnavailable
			return match
		}
		if !strings.EqualFold(r.Currency, basis.Currency) {
			match.Outcome = DecisionOutcomeUnavailable
			return match
		}
	}
	if unit != "" && basis.Unit != unit {
		match.Outcome = DecisionOutcomeUnavailable
		return match
	}
	if r.Limit.IsMoney() && basis.Currency != "" && !strings.EqualFold(r.Limit.Currency, basis.Currency) {
		match.Outcome = DecisionOutcomeUnavailable
		return match
	}
	if basis.Value > r.Limit.Value {
		match.Exceeded = true
		switch r.Mode {
		case RuleModeAdvisory:
			match.Outcome = DecisionOutcomeAdvisory
		default:
			if r.Kind == RuleKindSpendCap {
				match.Outcome = DecisionOutcomeClamp
				match.RequestedMax = basis
			} else {
				match.Outcome = DecisionOutcomeDeny
			}
		}
		return match
	}
	match.Outcome = DecisionOutcomeAllow
	return match
}

func (r Rule) matchesAuthority(authority AuthorityLevel) bool {
	switch r.AuthorityRequirement {
	case AuthorityRequirementAny:
		return true
	case AuthorityRequirementEstimated:
		return authority == AuthorityLevelEstimated || authority == AuthorityLevelAuthoritative
	case AuthorityRequirementAuthoritative:
		return authority == AuthorityLevelAuthoritative
	default:
		return false
	}
}

// SupportsAuthority reports whether this rule can consume a usage fact at the
// supplied authority level. It is intentionally read-only so application
// orchestration can filter unreserved usage without duplicating rule semantics.
func (r Rule) SupportsAuthority(authority AuthorityLevel) bool {
	return r.matchesAuthority(authority)
}
