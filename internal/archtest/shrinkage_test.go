package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement 11.5-11.9 shrinkage evidence and machine-checkable gate.
// ADR 0008 connector-architecture overlay is measured and ratcheted separately
// from the legacy convergence delta (main-spec historical ≥800-line target).

func TestShrinkage_BaselineInventoryLocked(t *testing.T) {
	t.Parallel()
	if RuntimeConvergenceShrinkageBaselineSHA != "efe4624909cea318c7211d5cb3734059d3210802" {
		t.Fatalf("baseline SHA drift: %s", RuntimeConvergenceShrinkageBaselineSHA)
	}
	if RuntimeConvergenceMinNetLineReduction != 800 {
		t.Fatalf("min reduction drift: %d", RuntimeConvergenceMinNetLineReduction)
	}
	if ConnectorArchitectureOverlayMax != 1000 {
		t.Fatalf("connector overlay cap drift: %d", ConnectorArchitectureOverlayMax)
	}
	if GenericCompatibleBackendOverlayMax != 900 {
		t.Fatalf("generic compatible overlay cap drift: %d", GenericCompatibleBackendOverlayMax)
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

func TestShrinkage_ConnectorOverlayExactMeasured(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	overlay, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	if overlay.Lines > ConnectorArchitectureOverlayMax {
		t.Fatalf("connector overlay: measured %d exceeds cap %d (files=%v)", overlay.Lines, ConnectorArchitectureOverlayMax, overlay.Files)
	}
	if !overlay.Pass {
		t.Fatal("overlay Pass must be true when lines <= Max")
	}
	if len(overlay.Files) == 0 {
		t.Fatal("overlay must select at least one production file")
	}
	for _, f := range overlay.Files {
		if !strings.HasPrefix(f, "internal/infra/runtimebundle/") {
			t.Fatalf("overlay file outside expected host/discovery surface: %s", f)
		}
		if strings.HasSuffix(f, "_test.go") {
			t.Fatalf("overlay must not include tests: %s", f)
		}
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
	if m.ConvergenceDelta != m.Delta-m.Overlay.Lines-m.GenericCompatibleOverlay.Lines {
		t.Fatalf("convergence delta inconsistency: got %d want %d-%d-%d", m.ConvergenceDelta, m.Delta, m.Overlay.Lines, m.GenericCompatibleOverlay.Lines)
	}
	wantPass := m.ConvergenceDelta <= m.RequiredMax && m.Overlay.Pass && m.GenericCompatibleOverlay.Pass
	if m.Pass != wantPass {
		t.Fatalf("pass flag inconsistency: pass=%v convergence=%+d overlay=%d", m.Pass, m.ConvergenceDelta, m.Overlay.Lines)
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
		"ADR 0008 connector-architecture overlay",
		"Convergence delta (raw − overlays):",
		"Required: convergence delta ≤ -800",
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
		t.Fatalf("Req 11.5 FAIL: raw delta %+d; overlay %d/%d; convergence delta %+d (need ≤ %+d)\n%sbaseline_total=%d current_total=%d overlay_files=%v",
			m.Delta, m.Overlay.Lines, m.Overlay.Max, m.ConvergenceDelta, m.RequiredMax,
			b.String(), m.BaselineTotal, m.CurrentTotal, m.Overlay.Files)
	}
}
