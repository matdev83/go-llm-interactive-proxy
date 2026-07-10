package domain

import (
	"strings"
	"time"
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
}

type EvaluationResult struct {
	Matches  []RuleMatch
	Selected RuleMatch
}

func EvaluateRules(rules []Rule, ctx EvaluationContext) EvaluationResult {
	result := EvaluationResult{Selected: RuleMatch{Outcome: DecisionOutcomeAllow}}
	matchedIDs := make([]string, 0, len(rules))

	for _, rule := range rules {
		if !rule.Matches(ctx.Dimensions) || !rule.matchesAuthority(ctx.Authority) {
			continue
		}
		match := rule.evaluate(ctx)
		result.Matches = append(result.Matches, match)
		matchedIDs = append(matchedIDs, rule.ID)
		if match.Outcome.severity() > result.Selected.Outcome.severity() {
			result.Selected = match
		}
	}

	if len(matchedIDs) > 0 {
		for i := range result.Matches {
			result.Matches[i].RuleIDs = append([]string(nil), matchedIDs...)
		}
		result.Selected.RuleIDs = append([]string(nil), matchedIDs...)
	}
	return result
}

func (r Rule) evaluate(ctx EvaluationContext) RuleMatch {
	match := RuleMatch{RuleID: r.ID, Kind: r.Kind, Mode: r.Mode}
	if !r.Matches(ctx.Dimensions) || !r.matchesAuthority(ctx.Authority) {
		return match
	}

	unit := r.Unit
	if unit == "" {
		unit = r.Limit.Unit
	}

	basis := ctx.Amount
	if unit == AmountUnitRequests && ctx.RequestCount.Unit != "" {
		basis = ctx.RequestCount
	} else if r.Kind == RuleKindBudget || r.Kind == RuleKindSpendCap {
		basis = ctx.Spend
		if strings.TrimSpace(basis.Currency) == "" {
			match.Outcome = DecisionOutcomeUnavailable
			return match
		}
		if !strings.EqualFold(r.Currency, basis.Currency) {
			match.Outcome = DecisionOutcomeUnavailable
			return match
		}
	} else if unit != "" && basis.Unit != unit {
		if amount, ok := ctx.PreflightUsage.AmountForUnit(unit); ok {
			basis = amount
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
