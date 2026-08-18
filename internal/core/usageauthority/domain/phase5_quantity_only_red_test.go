package domain

import (
	"strings"
	"testing"
)

func TestPhase5RetiredMoneyRuleRequiresMigrationError(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:    "legacy.money",
		Kind:  RuleKind("budget"),
		Mode:  RuleModeStrict,
		Unit:  AmountUnit("money_nano"),
		Limit: Amount{Unit: AmountUnit("money_nano"), Value: 100},
		Basis: BasisLegacyProviderPreferredAttempt,
	}
	err := rule.Validate()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "migration") {
		t.Fatalf("retired monetary rule error = %v, want explicit migration-required error", err)
	}
}

func TestPhase5QuantityRuleStillValidates(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:    "tenant.requests",
		Kind:  RuleKindQuota,
		Mode:  RuleModeStrict,
		Unit:  AmountUnitRequests,
		Limit: Amount{Unit: AmountUnitRequests, Value: 10},
		Basis: BasisLegacyProviderPreferredAttempt,
	}
	if err := rule.Validate(); err != nil {
		t.Fatalf("quantity rule validation failed: %v", err)
	}
}
