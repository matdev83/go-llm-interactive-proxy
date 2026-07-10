package config_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestValidateAccountingAuthorityDisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate disabled default: %v", err)
	}
	if cfg.Accounting.Authority.Enabled {
		t.Fatal("authority should remain disabled by default")
	}
	if cfg.Accounting.Authority.Query.Enabled {
		t.Fatal("authority query should remain disabled by default")
	}
}

func TestValidateAccountingAuthorityRejectsStrictRuleInAdvisoryMode(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("x", 12)},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Mode:    "advisory",
				Store:   "memory",
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Match: config.AccountingAuthorityDimensionsConfig{Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")}},
					},
				},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected advisory/strict mismatch to fail validation")
	}
	if !strings.Contains(err.Error(), "strict rules require accounting.authority.mode=strict") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccountingAuthorityRejectsQueryWithoutSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Store:   "memory",
				Query:   config.AccountingAuthorityQueryConfig{Enabled: true, PathPrefix: "/authority"},
				Rules:   []config.AccountingAuthorityRuleConfig{},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected authority query without shared secret to fail")
	}
	if !strings.Contains(err.Error(), "diagnostics.shared_secret") && !strings.Contains(err.Error(), "accounting.authority.query.enabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccountingAuthorityRejectsLargeMaxPageSize(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("x", 12)},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Store:   "memory",
				Query: config.AccountingAuthorityQueryConfig{
					Enabled:     true,
					PathPrefix:  "/authority",
					MaxPageSize: 150,
				},
				Rules: []config.AccountingAuthorityRuleConfig{},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected authority query with max page size > 100 to fail")
	}
	if !strings.Contains(err.Error(), "max_page_size: must not exceed 100") {
		t.Fatalf("unexpected error: %v", err)
	}
}
