package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement 11.5-11.9 shrinkage evidence and machine-checkable gate.

func TestShrinkage_BaselineInventoryLocked(t *testing.T) {
	t.Parallel()
	if RuntimeConvergenceShrinkageBaselineSHA != "efe4624909cea318c7211d5cb3734059d3210802" {
		t.Fatalf("baseline SHA drift: %s", RuntimeConvergenceShrinkageBaselineSHA)
	}
	if RuntimeConvergenceMinNetLineReduction != 800 {
		t.Fatalf("min reduction drift: %d", RuntimeConvergenceMinNetLineReduction)
	}
	want := []AffectedSurfaceBaseline{
		{Tree: "internal/infra/runtimebundle", BaselineLines: 9898},
		{Tree: "internal/infra/runtimehost", BaselineLines: 3056},
		{Tree: "internal/stdhttp", BaselineLines: 4666},
		{Tree: "cmd/lipstd", BaselineLines: 985},
		{Tree: "pkg/lipruntime", BaselineLines: 1037},
	}
	if len(RuntimeConvergenceAffectedSurfaces) != len(want) {
		t.Fatalf("surface count: got %d want %d", len(RuntimeConvergenceAffectedSurfaces), len(want))
	}
	sum := 0
	for i, got := range RuntimeConvergenceAffectedSurfaces {
		if got != want[i] {
			t.Fatalf("surface[%d]: got %+v want %+v", i, got, want[i])
		}
		sum += got.BaselineLines
	}
	if sum != 19642 {
		t.Fatalf("baseline total: got %d want 19642", sum)
	}
}

func TestShrinkage_MeasureDeterministicTotals(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.BaselineTotal != 19642 {
		t.Fatalf("baseline total: got %d want 19642", m.BaselineTotal)
	}
	if m.RequiredMax != -800 {
		t.Fatalf("required max delta: got %d want -800", m.RequiredMax)
	}
	if len(m.Surfaces) != 5 {
		t.Fatalf("surfaces: got %d want 5", len(m.Surfaces))
	}
	recomputed := 0
	for _, s := range m.Surfaces {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(s.Tree)))
		if err != nil {
			t.Fatal(err)
		}
		if s.CurrentLines != n {
			t.Fatalf("%s: measurement %d != recount %d", s.Tree, s.CurrentLines, n)
		}
		if s.Delta != s.CurrentLines-s.BaselineLines {
			t.Fatalf("%s: delta inconsistency %d", s.Tree, s.Delta)
		}
		recomputed += s.CurrentLines
	}
	if m.CurrentTotal != recomputed {
		t.Fatalf("current total: got %d want %d", m.CurrentTotal, recomputed)
	}
	if m.Delta != m.CurrentTotal-m.BaselineTotal {
		t.Fatalf("aggregate delta inconsistency: %d", m.Delta)
	}
	if m.Pass != (m.Delta <= m.RequiredMax) {
		t.Fatalf("pass flag inconsistency: pass=%v delta=%d", m.Pass, m.Delta)
	}
}

func TestShrinkage_ReportSectionIncludesVerdict(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	section, m, err := FormatRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"## Runtime-convergence net shrinkage (Req 11.5)",
		"Baseline SHA: `" + RuntimeConvergenceShrinkageBaselineSHA + "`",
		"| **TOTAL** |",
		"Required: delta ≤ -800",
	} {
		if !strings.Contains(section, needle) {
			t.Fatalf("report missing %q\n%s", needle, section)
		}
	}
	if m.Pass {
		if !strings.Contains(section, "Verdict: **PASS**") {
			t.Fatalf("expected PASS verdict in report:\n%s", section)
		}
	} else if !strings.Contains(section, "Verdict: **FAIL**") {
		t.Fatalf("expected FAIL verdict in report:\n%s", section)
	}
}

func TestShrinkage_NetReductionMeetsRequirement115(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Pass {
		var b strings.Builder
		for _, s := range m.Surfaces {
			fmt.Fprintf(&b, "  %s: %d -> %d (%+d)\n", s.Tree, s.BaselineLines, s.CurrentLines, s.Delta)
		}
		t.Fatalf("Req 11.5 FAIL: five-surface non-test delta %+d (need ≤ %+d; short by %d)\n%sbaseline_total=%d current_total=%d",
			m.Delta, m.RequiredMax, m.Delta-m.RequiredMax, b.String(), m.BaselineTotal, m.CurrentTotal)
	}
}
