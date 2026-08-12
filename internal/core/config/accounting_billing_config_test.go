package config

import (
	"testing"

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
