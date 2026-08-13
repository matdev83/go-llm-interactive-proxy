package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestAccountingBillingYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	const y = `
accounting:
  billing:
    authoritative: true
    reports_path: /admin/billing-custom
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Accounting.Billing.Authoritative {
		t.Fatal("accounting.billing.authoritative must round-trip true")
	}
	if cfg.Accounting.Billing.ReportsPath != "/admin/billing-custom" {
		t.Fatalf("reports_path = %q", cfg.Accounting.Billing.ReportsPath)
	}
}

func TestAccountingBillingHoldTTLYAML(t *testing.T) {
	t.Parallel()
	const y = `
accounting:
  billing:
    hold_ttl: 30m
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Accounting.Billing.HoldTTL != "30m" {
		t.Fatalf("hold_ttl = %q", cfg.Accounting.Billing.HoldTTL)
	}
	if got := cfg.Accounting.Billing.EffectiveHoldTTL(); got != 30*time.Minute {
		t.Fatalf("EffectiveHoldTTL = %s", got)
	}
	if got := (AccountingBillingConfig{}).EffectiveHoldTTL(); got != DefaultHoldTTL {
		t.Fatalf("default EffectiveHoldTTL = %s", got)
	}
}
