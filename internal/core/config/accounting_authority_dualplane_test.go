package config_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestValidateAccountingAuthorityAcceptsDualPlaneRule(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Rules: []config.AccountingAuthorityRuleConfig{{
					ID:             "customer.requests",
					Kind:           "quota",
					Mode:           "strict",
					Unit:           "requests",
					Limit:          10,
					Perspective:    "customer",
					LifecycleScope: "logical_request",
					Basis:          "frontend_ingress",
					Namespace:      "usage-authority/v2",
					Version:        "1",
					Match: config.AccountingAuthorityDimensionsConfig{
						Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
					},
				}},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("dual-plane rule should validate: %v", err)
	}
}

func TestValidateAccountingAuthorityRejectsAmbiguousRule(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Rules: []config.AccountingAuthorityRuleConfig{{
					ID:    "ambiguous",
					Kind:  "quota",
					Mode:  "strict",
					Unit:  "requests",
					Limit: 10,
				}},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected ambiguous rule rejection")
	}
	if !strings.Contains(err.Error(), "legacy_provider_preferred_attempt") {
		t.Fatalf("want migration hint, got %v", err)
	}
}
