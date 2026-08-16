package runtimebundle_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// TestBuildHost_LeftoverLedgerPricingDoesNotOpenBillingJournal proves leftover
// accounting.ledger.* and accounting.pricing YAML with authoritative unset is
// not a billing factory (req 1.3, 3.5).
func TestBuildHost_LeftoverLedgerPricingDoesNotOpenBillingJournal(t *testing.T) {
	t.Parallel()
	ledgerPath := filepath.Join(t.TempDir(), "leftover-token-ledger.db")
	cfgPath := writeLeftoverLedgerPricingConfig(t, ledgerPath)

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost with leftover ledger+pricing YAML: %v", err)
	}
	hostServeCleanup(t, host)

	if host.Config() == nil {
		t.Fatal("expected loaded config")
	}
	acc := host.Config().Accounting
	if acc.Billing.Authoritative {
		t.Fatal("leftover YAML must leave accounting.billing.authoritative unset/false")
	}
	if acc.Ledger.Store == "" || len(acc.Pricing.Models) == 0 {
		t.Fatal("test fixture must include leftover ledger and pricing YAML")
	}
	if host.HasProductionBillingStore() {
		t.Fatal("leftover ledger+pricing YAML must not inject BillingStore")
	}
	ex := hostActiveExecutor(t, host)
	if ex.BillingAuthoritative {
		t.Fatal("leftover ledger+pricing YAML must leave BillingAuthoritative false")
	}
	if ex.BillingExposureAdmission != nil || ex.CallUsageAppender != nil || ex.CallLegUsageAppender != nil {
		t.Fatal("leftover ledger+pricing YAML must not wire authoritative exposure ports")
	}
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatalf("leftover accounting.ledger YAML opened sqlite at %s: %v", ledgerPath, err)
	}
}

func writeLeftoverLedgerPricingConfig(t *testing.T, ledgerPath string) string {
	t.Helper()
	basePath := runtimebundle.MaterializeExampleConfigForTest(
		t,
		filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
	)
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	block := fmt.Sprintf(`
accounting:
  enabled: true
  mode: provider_first
  tokenizer:
    default_encoding: cl100k_base
  ledger:
    store: sqlite
    sqlite_path: %q
    write_policy: required
  pricing:
    currency: USD
    catalog_version: leftover-yaml-not-tur-catalog
    models:
      - backend: dogfood-local
        model: stub-default
        input_per_1m: "1.00"
        output_per_1m: "2.00"
`, filepath.ToSlash(ledgerPath))
	path := filepath.Join(t.TempDir(), "leftover-ledger-pricing.yaml")
	if err := os.WriteFile(path, append(append([]byte{}, raw...), []byte(block)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
