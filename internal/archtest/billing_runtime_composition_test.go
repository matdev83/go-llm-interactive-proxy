package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 7.1 removes the runtime mode selector. Billing is enabled only by the
// composition root's all-or-none port contract; production code must not carry
// a legacy/current boolean or infer billing from partial ports.
func TestBillingRuntimeHasNoModeSelectorInProductionComposition(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := []string{
		"internal/core/runtime/executor_config.go",
		"internal/core/runtime/billing_admission.go",
		"internal/infra/runtimebundle/production_options.go",
		"internal/infra/runtimebundle/build_executor.go",
		"internal/infra/runtimebundle/billing_compose.go",
		"internal/infra/runtimebundle/candidate_compile.go",
	}
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(raw), "BillingAuthoritative") ||
			strings.Contains(string(raw), "Accounting.Billing.Authoritative") {
			t.Errorf("%s retains a billing mode selector", rel)
		}
	}
}
