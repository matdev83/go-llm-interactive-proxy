package domain_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestAppliesToLifecycle_LegacyInfersFromUnit(t *testing.T) {
	t.Parallel()
	req := domain.Rule{
		ID:    "req",
		Kind:  domain.RuleKindQuota,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Basis: domain.BasisLegacyProviderPreferredAttempt,
	}
	tok := domain.Rule{
		ID:    "tok",
		Kind:  domain.RuleKindQuota,
		Unit:  domain.AmountUnitInputTokens,
		Limit: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 1},
		Basis: domain.BasisLegacyProviderPreferredAttempt,
	}
	if !req.AppliesToLifecycle(metering.LifecycleLogicalRequest) || req.AppliesToLifecycle(metering.LifecycleBackendAttempt) {
		t.Fatal("request-count legacy rule should apply only at logical_request")
	}
	if tok.AppliesToLifecycle(metering.LifecycleLogicalRequest) || !tok.AppliesToLifecycle(metering.LifecycleBackendAttempt) {
		t.Fatal("token legacy rule should apply only at backend_attempt")
	}
	if !req.AppliesToLifecycle("") {
		t.Fatal("empty stage disables filtering")
	}
}

func TestSelectAmount_DualPlaneFromExposure(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{
		ID:             "op-spend",
		Kind:           domain.RuleKindSpendCap,
		Unit:           domain.AmountUnitMoneyNano,
		Limit:          domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "usd"},
		Currency:       "usd",
		Perspective:    metering.PerspectiveOperator,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Basis:          domain.BasisBackendIngress,
		Namespace:      domain.NamespaceDefault,
	}
	amt, ok := rule.SelectAmount(domain.AmountSelectionSource{
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Money:       economics.Money{NanoUnits: 40, Currency: "usd", Present: true},
		},
	})
	if !ok || amt.Value != 40 || amt.Unit != domain.AmountUnitMoneyNano {
		t.Fatalf("got ok=%v amt=%v", ok, amt)
	}
}

func TestSelectAmount_MissingDualPlaneBasisUnavailable(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{
		ID:             "cust",
		Kind:           domain.RuleKindQuota,
		Unit:           domain.AmountUnitInputTokens,
		Limit:          domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 10},
		Perspective:    metering.PerspectiveCustomer,
		LifecycleScope: metering.LifecycleLogicalRequest,
		Basis:          domain.BasisFrontendIngress,
		Namespace:      domain.NamespaceDefault,
	}
	_, ok := rule.SelectAmount(domain.AmountSelectionSource{})
	if ok {
		t.Fatal("missing exposure/facts must not select zero")
	}
}

func TestEvaluateRules_StageFilterSkipsWrongLifecycle(t *testing.T) {
	t.Parallel()
	customer := domain.Rule{
		ID:             "cust-req",
		Kind:           domain.RuleKindQuota,
		Mode:           domain.RuleModeStrict,
		Unit:           domain.AmountUnitRequests,
		Limit:          domain.Amount{Unit: domain.AmountUnitRequests, Value: 5},
		Perspective:    metering.PerspectiveCustomer,
		LifecycleScope: metering.LifecycleLogicalRequest,
		Basis:          domain.BasisFrontendIngress,
		Namespace:      domain.NamespaceDefault,
	}
	operator := domain.Rule{
		ID:             "op-tok",
		Kind:           domain.RuleKindQuota,
		Mode:           domain.RuleModeStrict,
		Unit:           domain.AmountUnitInputTokens,
		Limit:          domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 100},
		Perspective:    metering.PerspectiveOperator,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Basis:          domain.BasisBackendIngress,
		Namespace:      domain.NamespaceDefault,
	}
	got := domain.EvaluateRules([]domain.Rule{customer, operator}, domain.EvaluationContext{
		LifecycleScope: metering.LifecycleLogicalRequest,
		RequestCount:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
		},
	})
	if len(got.Matches) != 1 || got.Matches[0].RuleID != "cust-req" {
		t.Fatalf("matches=%v", got.Matches)
	}
}
