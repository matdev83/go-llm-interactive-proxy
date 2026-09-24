package archtest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Remediation 3C fork-anchored verification and injected table negatives
// (Req 17.1-17.6, 18.1, 18.6).
//
// Sums alone cannot catch offsetting baseline edits, so every manifest entry
// is verified mechanically against the pinned feature-fork tree. Injected
// negative fixtures prove each table rule rejects independently.

// economicsGrowthForkSHA is the feature-fork/main merge-base whose per-file
// line counts anchor the manifest baselines. Immutable history: do not change.
const economicsGrowthForkSHA = "c7fa416950ef342d417b34db52107cc2fff15396"

// forkGrowthEntryError verifies one manifest entry against the fork tree:
// baseline 0 with provenance "new" is valid only for paths genuinely absent
// at the fork; present paths must carry the exact fork line count with
// provenance "modified". Empty means valid.
func forkGrowthEntryError(fs archtestFS, e economicsConvergenceGrowthEntry) string {
	src, err := fs.ReadFile(e.path)
	if err != nil {
		if os.IsNotExist(err) {
			if e.baseline != 0 || e.provenance != "new" {
				return fmt.Sprintf("growth manifest %s absent at fork must carry baseline 0/new, got %d/%s",
					e.path, e.baseline, e.provenance)
			}
			return ""
		}
		return fmt.Sprintf("growth manifest %s fork read: %v", e.path, err)
	}
	if n := countBytesLines(src); n != e.baseline {
		return fmt.Sprintf("growth manifest %s baseline %d != fork lines %d", e.path, e.baseline, n)
	}
	if e.provenance != "modified" {
		return fmt.Sprintf("growth manifest %s present at fork must carry provenance modified, got %q",
			e.path, e.provenance)
	}
	return ""
}

// mustGrowthEntry returns the manifest entry for path or fails the test.
func mustGrowthEntry(t *testing.T, path string) economicsConvergenceGrowthEntry {
	t.Helper()
	for _, e := range economicsConvergenceGrowthManifest {
		if e.path == path {
			return e
		}
	}
	t.Fatalf("manifest entry missing: %s", path)
	return economicsConvergenceGrowthEntry{}
}

// TestBillingEconomicsGrowthForkBaselinesExact verifies EVERY manifest entry's
// baseline and provenance against the pinned fork tree, so two baseline edits
// cannot offset inside the sums.
func TestBillingEconomicsGrowthForkBaselinesExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	fs, err := loadGitCommitFS(root, economicsGrowthForkSHA)
	if err != nil {
		t.Fatalf("load fork tree %s: %v", economicsGrowthForkSHA, err)
	}
	var modified, fresh int
	for _, e := range economicsConvergenceGrowthManifest {
		if msg := forkGrowthEntryError(fs, e); msg != "" {
			t.Errorf("fork verification: %s", msg)
			continue
		}
		if e.provenance == "new" {
			fresh++
		} else {
			modified++
		}
	}
	t.Logf("fork %s: %d verified entries (%d modified, %d new)", economicsGrowthForkSHA, modified+fresh, modified, fresh)
}

// TestBillingEconomicsGrowthForkRejectsContentMismatch proves preexisting or
// moved content cannot receive baseline-0/new-file credit: a forged baseline-0
// claim at a fork-preexisting destination is rejected, as are an off-by-one
// baseline and a wrong provenance.
func TestBillingEconomicsGrowthForkRejectsContentMismatch(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	fs, err := loadGitCommitFS(root, economicsGrowthForkSHA)
	if err != nil {
		t.Fatalf("load fork tree %s: %v", economicsGrowthForkSHA, err)
	}
	genuine := mustGrowthEntry(t, "internal/core/billing/account.go")
	if genuine.baseline <= 0 || genuine.provenance != "modified" {
		t.Fatalf("fixture entry must be fork-preexisting, got %+v", genuine)
	}
	forgedNew := genuine
	forgedNew.baseline = 0
	forgedNew.provenance = "new"
	if msg := forkGrowthEntryError(fs, forgedNew); msg == "" {
		t.Fatal("baseline-0/new claim for fork-preexisting content must be rejected, got pass")
	}
	forgedBaseline := genuine
	forgedBaseline.baseline++
	if msg := forkGrowthEntryError(fs, forgedBaseline); msg == "" {
		t.Fatal("off-by-one baseline must be rejected, got pass")
	}
	forgedProvenance := genuine
	forgedProvenance.provenance = "new"
	if msg := forkGrowthEntryError(fs, forgedProvenance); msg == "" {
		t.Fatal("new provenance for fork-preexisting content must be rejected, got pass")
	}
	if msg := forkGrowthEntryError(fs, genuine); msg != "" {
		t.Fatalf("genuine entry must verify, got %q", msg)
	}
}

// TestBillingEconomicsGrowthManifestNegativesInjected proves each table rule
// rejects with injected fixtures: duplicate path, unsorted order, empty path,
// unknown category, unknown provenance, new-with-baseline, modified with zero
// baseline, and negative credit.
func TestBillingEconomicsGrowthManifestNegativesInjected(t *testing.T) {
	t.Parallel()
	valid := economicsConvergenceGrowthEntry{
		path: "internal/core/billing/aa_probe.go", baseline: 10, credit: 1, category: "rating", provenance: "modified",
	}
	validNew := economicsConvergenceGrowthEntry{
		path: "internal/core/billing/ab_probe.go", baseline: 0, credit: 7, category: "query", provenance: "new",
	}
	cases := []struct {
		name    string
		entries []economicsConvergenceGrowthEntry
		pass    bool
	}{
		{name: "valid entries pass", entries: []economicsConvergenceGrowthEntry{valid, validNew}, pass: true},
		{
			name:    "duplicate path fails",
			entries: []economicsConvergenceGrowthEntry{valid, valid},
			pass:    false,
		},
		{
			name:    "unsorted order fails",
			entries: []economicsConvergenceGrowthEntry{validNew, valid},
			pass:    false,
		},
		{
			name: "empty path fails",
			entries: []economicsConvergenceGrowthEntry{
				{path: "", baseline: 1, credit: 0, category: "package", provenance: "modified"},
			},
			pass: false,
		},
		{
			name: "unknown category fails",
			entries: []economicsConvergenceGrowthEntry{
				{path: "internal/core/billing/aa_probe.go", baseline: 1, credit: 0, category: "bogus", provenance: "modified"},
			},
			pass: false,
		},
		{
			name: "unknown provenance fails",
			entries: []economicsConvergenceGrowthEntry{
				{path: "internal/core/billing/aa_probe.go", baseline: 1, credit: 0, category: "rating", provenance: "bogus"},
			},
			pass: false,
		},
		{
			name: "new with baseline fails",
			entries: []economicsConvergenceGrowthEntry{
				{path: "internal/core/billing/aa_probe.go", baseline: 5, credit: 5, category: "rating", provenance: "new"},
			},
			pass: false,
		},
		{
			name: "modified with zero baseline fails",
			entries: []economicsConvergenceGrowthEntry{
				{path: "internal/core/billing/aa_probe.go", baseline: 0, credit: 5, category: "rating", provenance: "modified"},
			},
			pass: false,
		},
		{
			name: "negative credit fails",
			entries: []economicsConvergenceGrowthEntry{
				{path: "internal/core/billing/aa_probe.go", baseline: 1, credit: -1, category: "rating", provenance: "modified"},
			},
			pass: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := validateEconomicsConvergenceGrowthManifest(tc.entries)
			if tc.pass && msg != "" {
				t.Fatalf("want pass, got %q", msg)
			}
			if !tc.pass && msg == "" {
				t.Fatal("want failure, got pass")
			}
		})
	}
}

// TestBillingEconomicsGrowthDisjointnessNegativesInjected proves overlap and
// scope rules reject with injected fixtures: a manifest copy claiming a
// whole-file overlay path, a followed-declaration file, or a path outside the
// denominator scope must fail.
func TestBillingEconomicsGrowthDisjointnessNegativesInjected(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingFinalConvergenceBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.IncludedDeclarations) == 0 {
		t.Fatal("artifact must carry followed declarations for the overlap fixture")
	}
	declFile := ""
	for _, d := range doc.IncludedDeclarations {
		if !strings.HasSuffix(d.File, "_test.go") {
			declFile = d.File
			break
		}
	}
	if declFile == "" {
		t.Fatal("no non-test declaration file found for the overlap fixture")
	}
	overlayEntry := economicsConvergenceGrowthEntry{
		path: "internal/infra/runtimebundle/production_options.go", baseline: 80, credit: 16,
		category: "composition", provenance: "modified",
	}
	declEntry := economicsConvergenceGrowthEntry{
		path: declFile, baseline: 1, credit: 1, category: "package", provenance: "modified",
	}
	scopeEntry := economicsConvergenceGrowthEntry{
		path: "internal/core/runtime/zz_outside_scope.go", baseline: 0, credit: 5,
		category: "terminal", provenance: "new",
	}
	withExtra := func(extra economicsConvergenceGrowthEntry) []economicsConvergenceGrowthEntry {
		out := make([]economicsConvergenceGrowthEntry, 0, len(economicsConvergenceGrowthManifest)+1)
		out = append(out, economicsConvergenceGrowthManifest...)
		out = append(out, extra)
		return out
	}
	t.Run("overlay overlap fails", func(t *testing.T) {
		t.Parallel()
		if msg := checkEconomicsConvergenceGrowthDisjointness(withExtra(overlayEntry), doc); msg == "" {
			t.Fatal("manifest copy overlapping the usage-economics overlay must fail, got pass")
		}
	})
	t.Run("declaration overlap fails", func(t *testing.T) {
		t.Parallel()
		if msg := checkEconomicsConvergenceGrowthDisjointness(withExtra(declEntry), doc); msg == "" {
			t.Fatalf("manifest copy overlapping declaration file %s must fail, got pass", declFile)
		}
	})
	t.Run("outside scope fails", func(t *testing.T) {
		t.Parallel()
		if msg := checkEconomicsConvergenceGrowthManifestScope(withExtra(scopeEntry), doc); msg == "" {
			t.Fatal("manifest copy outside the denominator scope must fail, got pass")
		}
	})
}
