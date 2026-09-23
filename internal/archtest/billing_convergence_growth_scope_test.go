package archtest

import (
	"path/filepath"
	"testing"
)

// Remediation 3C scope and disjointness proof (Req 17.1-17.6, 18.1, 18.6).
// Split from billing_convergence_growth_test.go at the 500-line
// maintainability cap; shared helpers live in that file. Live assertions run
// through the same scope/disjointness validators the injected negative tests
// exercise, so both enforce identical rules.

// TestBillingEconomicsGrowthScopeAndDisjointness ties every manifest entry to
// the artifact denominator scope (each entry in exactly one class:
// root-descendant XOR separately-followed file), proves live completeness
// (every enumerated denominator file is allowlisted), and enforces no double
// count against the whole-file usage-economics overlay and the
// followed-declaration inventory.
func TestBillingEconomicsGrowthScopeAndDisjointness(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	if msg := checkEconomicsConvergenceGrowthManifestScope(economicsConvergenceGrowthManifest, doc); msg != "" {
		t.Fatal(msg)
	}
	// Live completeness: every actual denominator file must be allowlisted.
	files, err := enumerateEconomicsConvergenceDenominatorFiles(root, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("live denominator enumeration must be non-empty")
	}
	allowed := make(map[string]struct{}, len(economicsConvergenceGrowthManifest))
	for _, e := range economicsConvergenceGrowthManifest {
		allowed[e.path] = struct{}{}
	}
	for _, f := range files {
		if _, ok := allowed[filepath.ToSlash(f)]; !ok {
			t.Fatalf("live denominator file outside pinned growth manifest: %s", f)
		}
	}
	if msg := checkEconomicsConvergenceGrowthDisjointness(economicsConvergenceGrowthManifest, doc); msg != "" {
		t.Fatal(msg)
	}
}
