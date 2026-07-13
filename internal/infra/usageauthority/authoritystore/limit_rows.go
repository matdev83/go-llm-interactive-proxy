package authoritystore

import (
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// LimitRowsFromRules derives live limit rows for store seeding from configured rules.
func LimitRowsFromRules(rules []domain.Rule, at time.Time) ([]controlplane.AccountingLimitStatusRow, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := make([]controlplane.AccountingLimitStatusRow, 0, len(rules))
	for _, rule := range rules {
		row, err := limitRowFromRule(rule, at)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		out = append(out, row)
	}
	return out, nil
}

func limitRowFromRule(rule domain.Rule, at time.Time) (controlplane.AccountingLimitStatusRow, error) {
	dims := dimensionsFromMatchers(rule.Match)
	scopeSnap := scopeSnapshotFromDimensions(dims)

	unit := rule.Unit
	if unit == "" {
		unit = rule.Limit.Unit
	}
	limit := rule.Limit.Value
	row := controlplane.AccountingLimitStatusRow{
		Scope:          scopeSnap,
		Correlation:    correlationFromMatchers(rule.Match),
		RuleID:         rule.ID,
		RuleType:       string(rule.Kind),
		Unit:           string(unit),
		Currency:       rule.Currency,
		Limit:          limit,
		Consumed:       0,
		Reserved:       0,
		Remaining:      limit,
		Authority:      authoritySourceForRule(rule),
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionSummarized,
	}
	if rule.Window.Algorithm != "" || rule.Window.Size != 0 || !rule.Window.Anchor.IsZero() {
		bounds, err := rule.Window.Bounds(at)
		if err != nil {
			return controlplane.AccountingLimitStatusRow{}, err
		}
		row.WindowStart = bounds.Start
		row.WindowEnd = bounds.End
		row.WindowResetAt = bounds.End
	}
	return row, nil
}

func dimensionsFromMatchers(m domain.DimensionsMatcher) domain.Dimensions {
	out := domain.Dimensions{
		Principal:    scopeFromMatcher(m.Principal),
		Credential:   scopeFromMatcher(m.Credential),
		Tenant:       scopeFromMatcher(m.Tenant),
		Organization: scopeFromMatcher(m.Organization),
		Workspace:    scopeFromMatcher(m.Workspace),
		Project:      scopeFromMatcher(m.Project),
		Department:   scopeFromMatcher(m.Department),
		CostCenter:   scopeFromMatcher(m.CostCenter),
		Backend:      scopeFromMatcher(m.Backend),
		Model:        scopeFromMatcher(m.Model),
		Route:        scopeFromMatcher(m.Route),
	}
	if len(m.Labels) > 0 {
		out.PolicyLabels = make(map[string]scope.Value, len(m.Labels))
		for key, matcher := range m.Labels {
			out.PolicyLabels[key] = scopeFromMatcher(matcher)
		}
	}
	return out
}

func scopeFromMatcher(m domain.DimensionMatcher) scope.Value {
	if m.MatchUnknown {
		return scope.Unknown()
	}
	if !m.Value.IsKnown() {
		return scope.Unknown()
	}
	return m.Value
}

func correlationFromMatchers(m domain.DimensionsMatcher) controlplane.Correlation {
	return controlplane.Correlation{
		BackendID: matcherCorrelationValue(m.Backend),
		Model:     matcherCorrelationValue(m.Model),
	}
}

func matcherCorrelationValue(m domain.DimensionMatcher) string {
	if m.MatchUnknown || !m.Value.IsKnown() {
		return ""
	}
	return m.Value.String()
}

func authoritySourceForRule(rule domain.Rule) controlplane.AccountingAuthoritySource {
	if rule.Mode == domain.RuleModeAdvisory {
		return controlplane.AccountingAuthoritySourceAdvisory
	}
	return controlplane.AccountingAuthoritySourceAuthoritative
}
