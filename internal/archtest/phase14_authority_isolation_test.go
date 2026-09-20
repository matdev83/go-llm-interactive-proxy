package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 14.3 certification: strict offers are rejected through the one monetary
// admission authority, supplier work never takes the customer balance lock,
// and stream handlers stay off all quote/settle/balance math.

// TestPhase14StreamFilesStayOffQuoteSettleAndBalance extends the standing
// stream-handler guard to the Phase 14 quote/settle symbols: no stream-time
// financial rating, admission math, or balance mutation path may exist.
func TestPhase14StreamFilesStayOffQuoteSettleAndBalance(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	streamFiles := []string{
		"executor_recv_loop.go",
		"executor_settlement.go",
		"executor_retry_stream.go",
		"response_pipeline_observations.go",
		"stream_terminal.go",
		"turn_terminal.go",
		"parallel_race.go",
	}
	forbiddenIdents := []string{
		"EstimateMaxCustomerCharge",
		"EstimateRichCustomerCharge",
		"EvaluateSettle",
		"EvaluateAdmit",
		"AdmitExposure",
		"ApplyCallBillingResult",
		"ApplyBalanceDelta",
		"AdmitExposureInput",
		"SettleExposureInput",
	}
	dir := filepath.Join(root, "internal", "core", "runtime")
	fset := token.NewFileSet()
	for _, name := range streamFiles {
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, forbid := range forbiddenIdents {
				if id.Name == forbid {
					t.Fatalf("%s must not reference %s (no stream-time quote/settle/balance path)", name, forbid)
				}
			}
			return true
		})
	}
}

// TestPhase14SingleMonetaryAdmissionAuthority locks Req 14.1/14.4: exactly one
// production implementation of the monetary exposure admission interface and
// exactly one durable exposure store may exist. Test fakes are excluded;
// non-money authority registrations live on different interfaces.
func TestPhase14SingleMonetaryAdmissionAuthority(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var authorityImpls []string
	var storeImpls []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(src)
		// Receiver definitions carry ") Admit(" / ") AdmitExposure("; the
		// port interface declarations do not, so they never match here.
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, ") Admit(") && strings.Contains(line, "BillingExposureAdmissionInput") {
				rel, _ := filepath.Rel(root, path)
				authorityImpls = append(authorityImpls, filepath.ToSlash(rel))
				break
			}
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, ") AdmitExposure(") {
				rel, _ := filepath.Rel(root, path)
				storeImpls = append(storeImpls, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(authorityImpls) != 1 || authorityImpls[0] != "internal/infra/billingadmission/adapter.go" {
		t.Fatalf("monetary admission authority impls = %v, want exactly [internal/infra/billingadmission/adapter.go]", authorityImpls)
	}
	if len(storeImpls) != 1 || storeImpls[0] != "internal/infra/billingstore/exposure_store.go" {
		t.Fatalf("durable exposure store impls = %v, want exactly [internal/infra/billingstore/exposure_store.go]", storeImpls)
	}
}

// TestPhase14ProviderCostProcessingTakesNoCustomerLock locks Req 14.6: supplier
// COGS processing must not acquire the customer admission account-balance
// lock nor write customer balances. Customer admission stays operable while
// supplier costing backlogs.
func TestPhase14ProviderCostProcessingTakesNoCustomerLock(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "billingstore")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "provider_cost_") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		for _, term := range []string{"lockAccount", "SET balance_nano", "balance_nano =", "SET version =", "UPDATE billing_accounts"} {
			if strings.Contains(body, term) {
				t.Fatalf("%s must not take the customer lock or write customer balances (%q)", name, term)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no provider_cost_*.go production files found")
	}
}

// TestPhase14RuntimeStaysOffQuoteAndSettleMath locks the authority boundary:
// quote mathematics lives in the admission adapter and settlement math in the
// billing domain/store. Runtime only carries the admission interface and
// terminal handoff, never the math.
func TestPhase14RuntimeStaysOffQuoteAndSettleMath(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		for _, term := range []string{
			"EstimateMaxCustomerCharge",
			"EstimateRichCustomerCharge",
			"EvaluateSettle",
			"EvaluateAdmit",
		} {
			if strings.Contains(body, term) {
				t.Fatalf("runtime %s must not contain quote/settle math (%q)", name, term)
			}
		}
	}
}
