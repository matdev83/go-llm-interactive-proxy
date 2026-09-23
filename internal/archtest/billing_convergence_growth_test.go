package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Remediation 3C per-file growth certification (Req 17.1-17.6, 18.1, 18.6).
//
// The billing-convergence denominator counts whole roots wholesale, so any
// unknown file inside an owned root would previously credit itself. These
// tests pin the complete per-file manifest (exact fork baselines at
// c7fa416950ef342d417b34db52107cc2fff15396, audited credits, requirement
// attribution) and prove on real filesystem trees that unknown descendants
// fail before credit, moved code cannot take baseline 0, deletions cannot
// free reusable allowance, and only max(0, current-baseline) per allowlisted
// entry counts toward the capped total.

// TestBillingEconomicsGrowthManifestLocked pins the remediation-3C allowance
// table: 160 entries, fork-baseline sum 9,593 (roots 8,209 + files 1,384 at
// c7fa4169), audited-credit sum 53,785, cap 53,846 (live-measured 53,821 + 25). Schema,
// order, uniqueness, and attribution rules run through the shared table
// validator so production and injected-negative tests enforce identical rules;
// per-entry fork values are verified mechanically against the pinned fork tree
// by TestBillingEconomicsGrowthForkBaselinesExact, so offsetting baseline edits
// cannot hide in the sums. Any broadening, rebasing, or attribution change
// requires an explicit table edit that review must approve.
func TestBillingEconomicsGrowthManifestLocked(t *testing.T) {
	t.Parallel()
	if len(economicsConvergenceGrowthManifest) != 160 {
		t.Fatalf("growth manifest entries = %d, want 160", len(economicsConvergenceGrowthManifest))
	}
	if EconomicsConvergenceGrowthOverlayMax != 53846 {
		t.Fatalf("growth cap drift: %d, want 53846", EconomicsConvergenceGrowthOverlayMax)
	}
	if msg := validateEconomicsConvergenceGrowthManifest(economicsConvergenceGrowthManifest); msg != "" {
		t.Fatalf("growth manifest schema rejected: %s", msg)
	}
	var sumBaseline, sumCredit int
	for _, e := range economicsConvergenceGrowthManifest {
		sumBaseline += e.baseline
		sumCredit += e.credit
	}
	if sumBaseline != 9593 {
		t.Fatalf("manifest baseline sum = %d, want 9593 (fork roots 8209 + files 1384)", sumBaseline)
	}
	if sumCredit != 53785 {
		t.Fatalf("manifest audited credit sum = %d, want 53785", sumCredit)
	}
	// Spot-check representative entries across roots and provenances so a
	// silent baseline/credit/category edit fails loudly.
	spot := []economicsConvergenceGrowthEntry{
		{path: "internal/core/billing/account.go", baseline: 102, credit: 0, category: "settlement", provenance: "modified"},
		{path: "internal/core/billing/append.go", baseline: 50, credit: 10, category: "lifecycle", provenance: "modified"},
		{path: "internal/core/billing/component_rater.go", baseline: 0, credit: 1652, category: "rating", provenance: "new"},
		{path: "internal/core/runtime/billing_leg.go", baseline: 417, credit: 270, category: "terminal", provenance: "modified"},
		{path: "internal/infra/billingadmission/adapter.go", baseline: 186, credit: 181, category: "admission", provenance: "modified"},
		{path: "internal/infra/billingcompose/catalog.go", baseline: 467, credit: 247, category: "composition", provenance: "modified"},
		{path: "internal/infra/billingstore/call_usage_store.go", baseline: 454, credit: 519, category: "persistence", provenance: "modified"},
		{path: "internal/infra/billingstore/reports_aleg.go", baseline: 0, credit: 2004, category: "query", provenance: "new"},
		{path: "internal/infra/billingstore/v2_economics_store.go", baseline: 0, credit: 1614, category: "persistence", provenance: "new"},
		{path: "internal/infra/runtimebundle/billing_compose.go", baseline: 116, credit: 65, category: "composition", provenance: "modified"},
	}
	byPath := make(map[string]economicsConvergenceGrowthEntry, len(economicsConvergenceGrowthManifest))
	for _, e := range economicsConvergenceGrowthManifest {
		byPath[e.path] = e
	}
	for _, want := range spot {
		got, ok := byPath[want.path]
		if !ok {
			t.Fatalf("spot entry missing from manifest: %s", want.path)
		}
		if got != want {
			t.Fatalf("spot entry drifted: got %+v, want %+v", got, want)
		}
	}
}

// TestBillingEconomicsGrowthAllowanceLive verifies the live allowance through
// the deletion-friendly predicate: cap pinned, every credited entry
// allowlisted, allowance within cap. The exact live total is deliberately not
// pinned so beneficial deletions keep passing.
func TestBillingEconomicsGrowthAllowanceLive(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	growth, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
	if err != nil {
		t.Fatal(err)
	}
	if msg := checkEconomicsConvergenceGrowthAllowance(growth); msg != "" {
		t.Fatal(msg)
	}
	if !growth.summary.Pass {
		t.Fatal("growth overlay Pass must be true when lines <= Max")
	}
	if growth.summary.Lines <= 0 {
		t.Fatal("live growth allowance must be positive on the feature branch")
	}
	if c := growth.credit["internal/core/billing/component_rater.go"]; c <= 0 {
		t.Fatalf("live credit for component_rater.go = %d, want > 0", c)
	}
}

// writeGrowthProbeLines writes a synthetic production .go file with exactly n
// physical lines.
func writeGrowthProbeLines(t *testing.T, root, rel string, n int) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("package probe\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "var probeVar%d = %d\n", i, i)
	}
	if err := os.WriteFile(abs, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// growthRootDoc builds a denominator doc covering a single owned root.
func growthRootDoc(rootPath string) BillingFinalConvergenceBaselineFile {
	return BillingFinalConvergenceBaselineFile{
		IncludedRoots: []BillingFinalConvergenceRoot{{ID: "probe", Path: rootPath}},
	}
}

// TestBillingEconomicsGrowthUnknownRejectedPerRoot proves on real filesystem
// trees that an unknown descendant inside EACH of the four owned roots fails
// enumeration before any credit is computed: unlisted code can never credit
// itself.
func TestBillingEconomicsGrowthUnknownRejectedPerRoot(t *testing.T) {
	t.Parallel()
	roots := []string{
		"internal/core/billing",
		"internal/infra/billingstore",
		"internal/infra/billingcompose",
		"internal/infra/billingadmission",
	}
	for _, r := range roots {
		t.Run(r, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeGrowthProbeLines(t, root, r+"/zz_unknown_growth_probe.go", 20)
			_, err := measureEconomicsConvergenceGrowthOverlay(root, growthRootDoc(r))
			if err == nil {
				t.Fatalf("unknown file in %s must fail enumeration, got pass", r)
			}
			if !strings.Contains(err.Error(), "outside pinned growth manifest") {
				t.Fatalf("unexpected error for unknown file in %s: %v", r, err)
			}
		})
	}
}

// TestBillingEconomicsGrowthMovedCodeRejected proves a moved/renamed baseline
// file cannot take baseline 0 at its new path: the unlisted destination fails
// enumeration even when it carries exactly the preexisting line count, and
// even when the original manifest path is simultaneously deleted.
func TestBillingEconomicsGrowthMovedCodeRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doc := growthRootDoc("internal/core/billing")
	// Original manifest path absent (simulates the move source deleted) while
	// the moved clone carries the old 102-line content at an unlisted path.
	writeGrowthProbeLines(t, root, "internal/core/billing/zz_moved_clone.go", 102)
	_, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
	if err == nil {
		t.Fatal("moved baseline code at an unlisted path must be rejected, got pass")
	}
	if !strings.Contains(err.Error(), "zz_moved_clone.go") {
		t.Fatalf("rejection must name the moved path, got: %v", err)
	}
}

// TestBillingEconomicsGrowthDeletionNoReuse proves deleting historical code
// cannot create reusable allowance: an unknown file still fails when a
// manifest file is simultaneously deleted, and the predicate still bounds
// every surviving entry independently.
func TestBillingEconomicsGrowthDeletionNoReuse(t *testing.T) {
	t.Parallel()
	t.Run("deleted file does not admit unknown growth", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		doc := growthRootDoc("internal/core/billing")
		// internal/core/billing/account.go (baseline 102) deleted live, plus
		// 20 lines of unrelated unlisted growth.
		writeGrowthProbeLines(t, root, "internal/core/billing/zz_unrelated_new.go", 20)
		_, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
		if err == nil {
			t.Fatal("unknown growth must fail even when a manifest file is deleted, got pass")
		}
	})
	t.Run("deleted entry does not raise another entry cap", func(t *testing.T) {
		t.Parallel()
		over := economicsConvergenceGrowthManifest[1] // accounting_cutover.go, credit 331
		result := economicsConvergenceGrowthResult{
			summary: OverlayMeasurement{
				Name:  "Economics convergence growth",
				Max:   EconomicsConvergenceGrowthOverlayMax,
				Lines: 100,
				Files: []string{over.path},
				Pass:  true,
			},
			// A sibling entry deleted (credit 0, omitted) while this entry
			// exceeds its own audited credit + 25.
			credit: map[string]int{over.path: over.credit + 26},
		}
		if msg := checkEconomicsConvergenceGrowthAllowance(result); msg == "" {
			t.Fatal("entry exceeding audited credit + 25 must fail despite sibling deletion, got pass")
		}
	})
}

// TestBillingEconomicsGrowthDeletedManifestFilePasses proves manifest files
// deleted live credit zero and pass: an empty denominator tree measures 0 and
// satisfies the predicate.
func TestBillingEconomicsGrowthDeletedManifestFilePasses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doc := BillingFinalConvergenceBaselineFile{
		IncludedRoots: []BillingFinalConvergenceRoot{
			{ID: "core-billing", Path: "internal/core/billing"},
			{ID: "billingstore", Path: "internal/infra/billingstore"},
			{ID: "billingcompose", Path: "internal/infra/billingcompose"},
			{ID: "billingadmission", Path: "internal/infra/billingadmission"},
		},
	}
	for _, r := range doc.IncludedRoots {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(r.Path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	growth, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
	if err != nil {
		t.Fatal(err)
	}
	if growth.summary.Lines != 0 || !growth.summary.Pass {
		t.Fatalf("deleted manifest files must credit zero and pass, got %+v", growth.summary)
	}
	if msg := checkEconomicsConvergenceGrowthAllowance(growth); msg != "" {
		t.Fatalf("deleted manifest files must satisfy the predicate, got %q", msg)
	}
}

// TestBillingEconomicsGrowthBeneficialDeletionPasses proves shrinking a file
// below its fork baseline credits zero and passes, while growth above the
// baseline credits exactly max(0, current-baseline). Credit cases run against
// a rooted denominator doc because only enumerated denominator members can
// credit; the empty-denominator case proves absence credits zero.
func TestBillingEconomicsGrowthBeneficialDeletionPasses(t *testing.T) {
	t.Parallel()
	emptyDoc := BillingFinalConvergenceBaselineFile{}
	scopedDoc := growthRootDoc("internal/infra/runtimebundle")

	t.Run("absent files credit zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, emptyDoc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 0 || !growth.summary.Pass {
			t.Fatalf("absent files must credit zero and pass, got %+v", growth.summary)
		}
	})

	t.Run("baseline content cannot enter", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeGrowthProbeLines(t, root, "internal/infra/runtimebundle/billing_compose.go", 116)
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, scopedDoc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 0 {
			t.Fatalf("baseline content must credit zero, got %+v", growth.summary)
		}
	})

	t.Run("shrunken file credits zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeGrowthProbeLines(t, root, "internal/infra/runtimebundle/billing_compose.go", 100)
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, scopedDoc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 0 {
			t.Fatalf("beneficial deletion must credit zero, got %+v", growth.summary)
		}
		if msg := checkEconomicsConvergenceGrowthAllowance(growth); msg != "" {
			t.Fatalf("beneficial deletion must satisfy the predicate, got %q", msg)
		}
	})

	t.Run("growth credits exactly", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeGrowthProbeLines(t, root, "internal/infra/runtimebundle/billing_compose.go", 116+40)
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, scopedDoc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 40 {
			t.Fatalf("growth credit = %d, want exactly 40", growth.summary.Lines)
		}
		if got := growth.credit["internal/infra/runtimebundle/billing_compose.go"]; got != 40 {
			t.Fatalf("per-entry credit = %d, want exactly 40", got)
		}
	})

	t.Run("outside enumerated scope invisible", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		doc := growthRootDoc("internal/core/billing")
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash("internal/core/billing")), 0o755); err != nil {
			t.Fatal(err)
		}
		// Under a tree the denominator doc does not enumerate: no credit and
		// no unknown-file failure.
		writeGrowthProbeLines(t, root, "internal/infra/billingstore/zz_outside_scope.go", 500)
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 0 {
			t.Fatalf("files outside the enumerated denominator scope must credit zero, got %+v", growth.summary)
		}
	})
}

// writeGrowthGeneratedLines writes a synthetic generated .go file with exactly
// n physical lines. The scanner classifier treats it as generated (excluded
// from the denominator) via the leading marker line.
func writeGrowthGeneratedLines(t *testing.T, root, rel string, n int) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("Code generated by probe. DO NOT EDIT.\n")
	b.WriteString("package probe\n")
	for i := 2; i < n; i++ {
		fmt.Fprintf(&b, "var probeGenVar%d = %d\n", i, i)
	}
	if err := os.WriteFile(abs, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBillingEconomicsGrowthGeneratedExcludedCreditsZero proves a manifest
// path classified as generated disappears from the denominator and receives
// zero credit even though it is allowlisted and longer than its baseline. The
// control subtest shows the same file without the marker would credit.
func TestBillingEconomicsGrowthGeneratedExcludedCreditsZero(t *testing.T) {
	t.Parallel()
	doc := growthRootDoc("internal/core/billing")

	t.Run("generated manifest file credits zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// internal/core/billing/account.go baseline is 102; 150 generated
		// lines would credit 48 if enumerated.
		writeGrowthGeneratedLines(t, root, "internal/core/billing/account.go", 150)
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 0 {
			t.Fatalf("excluded generated manifest file must credit zero, got %+v", growth.summary)
		}
		if msg := checkEconomicsConvergenceGrowthAllowance(growth); msg != "" {
			t.Fatalf("excluded generated manifest file must satisfy the predicate, got %q", msg)
		}
	})

	t.Run("control non-generated file credits", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeGrowthProbeLines(t, root, "internal/core/billing/account.go", 150)
		growth, err := measureEconomicsConvergenceGrowthOverlay(root, doc)
		if err != nil {
			t.Fatal(err)
		}
		if growth.summary.Lines != 48 {
			t.Fatalf("non-generated growth credit = %d, want exactly 48", growth.summary.Lines)
		}
	})
}

// growthPredicateResult builds a predicate input with the pinned name and cap.
func growthPredicateResult(lines int, files []string, credit map[string]int) economicsConvergenceGrowthResult {
	return economicsConvergenceGrowthResult{
		summary: OverlayMeasurement{
			Name:  "Economics convergence growth",
			Max:   EconomicsConvergenceGrowthOverlayMax,
			Lines: lines,
			Files: files,
			Pass:  lines <= EconomicsConvergenceGrowthOverlayMax,
		},
		credit: credit,
	}
}

// TestBillingEconomicsGrowthPredicateBounds runs the complete guard over
// reduced, deleted-file, over-cap, broadened, overlapping, duplicate,
// malformed, and per-entry over-cap measurements.
func TestBillingEconomicsGrowthPredicateBounds(t *testing.T) {
	t.Parallel()
	over := economicsConvergenceGrowthManifest[1] // accounting_cutover.go, credit 331
	allowedFile := economicsConvergenceGrowthManifest[0].path
	cases := []struct {
		name   string
		result economicsConvergenceGrowthResult
		pass   bool
	}{
		{
			name:   "reduced measurement passes",
			result: growthPredicateResult(30000, []string{allowedFile}, map[string]int{allowedFile: 0}),
			pass:   true,
		},
		{
			name:   "deleted allowlisted entry passes",
			result: growthPredicateResult(50000, []string{allowedFile}, map[string]int{}),
			pass:   true,
		},
		{
			name:   "cap plus one fails",
			result: growthPredicateResult(EconomicsConvergenceGrowthOverlayMax+1, []string{allowedFile}, map[string]int{allowedFile: 0}),
			pass:   false,
		},
		{
			name: "unknown broadened file fails",
			result: growthPredicateResult(100, []string{allowedFile, "internal/core/runtime/unlisted_feature.go"},
				map[string]int{allowedFile: 0}),
			pass: false,
		},
		{
			name: "unknown credit key fails",
			result: growthPredicateResult(100, []string{allowedFile},
				map[string]int{"internal/core/billing/unlisted_clone.go": 10}),
			pass: false,
		},
		{
			name:   "per-entry headroom boundary passes",
			result: growthPredicateResult(100, []string{over.path}, map[string]int{over.path: over.credit + 25}),
			pass:   true,
		},
		{
			name:   "per-entry cap plus one fails",
			result: growthPredicateResult(100, []string{over.path}, map[string]int{over.path: over.credit + 26}),
			pass:   false,
		},
		{
			name: "wrong overlay name fails",
			result: func() economicsConvergenceGrowthResult {
				r := growthPredicateResult(100, []string{allowedFile}, map[string]int{allowedFile: 0})
				r.summary.Name = "Wrong overlay"
				return r
			}(),
			pass: false,
		},
		{
			name: "wrong overlay max fails",
			result: func() economicsConvergenceGrowthResult {
				r := growthPredicateResult(100, []string{allowedFile}, map[string]int{allowedFile: 0})
				r.summary.Max = EconomicsConvergenceGrowthOverlayMax - 1
				return r
			}(),
			pass: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := checkEconomicsConvergenceGrowthAllowance(tc.result)
			if tc.pass && msg != "" {
				t.Fatalf("want pass, got %q", msg)
			}
			if !tc.pass && msg == "" {
				t.Fatal("want failure, got pass")
			}
		})
	}
}
