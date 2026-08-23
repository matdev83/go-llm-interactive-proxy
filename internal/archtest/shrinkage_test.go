package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement 11.5-11.9 shrinkage evidence and machine-checkable gate.
// ADR 0008 connector-architecture overlay is measured and ratcheted separately
// from the legacy convergence delta (main-spec historical ≥800-line target).
// Path-marker feature overlays are driven by a single table.

func TestShrinkage_BaselineInventoryLocked(t *testing.T) {
	t.Parallel()
	if RuntimeConvergenceShrinkageBaselineSHA != "efe4624909cea318c7211d5cb3734059d3210802" {
		t.Fatalf("baseline SHA drift: %s", RuntimeConvergenceShrinkageBaselineSHA)
	}
	if RuntimeConvergenceMinNetLineReduction != 800 {
		t.Fatalf("min reduction drift: %d", RuntimeConvergenceMinNetLineReduction)
	}
	if ConnectorArchitectureOverlayMax != 2300 {
		t.Fatalf("connector overlay cap drift: %d", ConnectorArchitectureOverlayMax)
	}
	if BackendResourcePoolOverlayMax != 381 {
		t.Fatalf("backend resource pool overlay cap drift: %d", BackendResourcePoolOverlayMax)
	}
	if GenericCompatibleBackendOverlayMax != 946 {
		t.Fatalf("generic compatible overlay cap drift: %d", GenericCompatibleBackendOverlayMax)
	}
	if BillingHostCompositionOverlayMax != 365 {
		t.Fatalf("billing host composition overlay cap drift: %d", BillingHostCompositionOverlayMax)
	}
	if AtomicOwnedResourceLifecycleOverlayMax != 92 {
		t.Fatalf("atomic owned resource lifecycle overlay cap drift: %d", AtomicOwnedResourceLifecycleOverlayMax)
	}
	if len(pathMarkerOverlaySpecs) != 6 {
		t.Fatalf("path-marker overlay table drift: got %d specs, want 6", len(pathMarkerOverlaySpecs))
	}
	if GeoIPIngressOverlayMax != 700 {
		t.Fatalf("GeoIP ingress overlay cap drift: %d", GeoIPIngressOverlayMax)
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

func TestShrinkage_GenericOverlayIgnoresSiblingWorktrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	production := filepath.Join(root, "internal", "core", "compatible_ownership.go")
	sibling := filepath.Join(root, ".worktrees", "other", "internal", "core", "compatible_ownership.go")
	tools := filepath.Join(root, "tools", "compatible_admission.go")
	vendor := filepath.Join(root, "vendor", "compatible_admission.go")
	for _, path := range []string{production, sibling, tools, vendor} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package core\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Generic compatible")
	want := []string{"internal/core/compatible_ownership.go", "tools/compatible_admission.go", "vendor/compatible_admission.go"}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] || overlay.Files[1] != want[1] || overlay.Files[2] != want[2] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 3 {
		t.Fatalf("overlay lines = %d, want 3", overlay.Lines)
	}
}

func TestShrinkage_AtomicOverlaySelectsPrimitives(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	production := filepath.Join(root, "internal", "infra", "runtimebundle", "process_owner.go")
	unrelated := filepath.Join(root, "internal", "infra", "runtimebundle", "build_persistence.go")
	for _, path := range []string{production, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package runtimebundle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Atomic owned resource lifecycle")
	want := []string{"internal/infra/runtimebundle/process_owner.go"}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 1 {
		t.Fatalf("overlay lines = %d, want 1", overlay.Lines)
	}
}

func TestShrinkage_BackendResourcePoolOverlaySelectsOnlyPoolFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	selected := filepath.Join(root, "internal", "infra", "runtimebundle", "backend_resource_pool.go")
	testFile := filepath.Join(root, "internal", "infra", "runtimebundle", "backend_resource_pool_test.go")
	unrelated := filepath.Join(root, "internal", "infra", "runtimebundle", "backend_resource_pool_helper.go")
	for _, path := range []string{selected, testFile, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package runtimebundle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Backend resource pool")
	if overlay.Max != 381 {
		t.Fatalf("overlay max = %d, want 381", overlay.Max)
	}
	want := []string{"internal/infra/runtimebundle/backend_resource_pool.go"}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 1 {
		t.Fatalf("overlay lines = %d, want 1", overlay.Lines)
	}
}

func pathOverlayByName(t *testing.T, root, name string) OverlayMeasurement {
	t.Helper()
	overlays, err := measurePathMarkerOverlays(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range overlays {
		if o.Name == name {
			return o
		}
	}
	names := make([]string, 0, len(overlays))
	for _, o := range overlays {
		names = append(names, o.Name)
	}
	t.Fatalf("path overlay %q not found (have %v)", name, names)
	return OverlayMeasurement{}
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
	overlayLines := m.Connector.Lines
	for _, o := range m.PathOverlays {
		overlayLines += o.Lines
	}
	if m.ConvergenceDelta != m.Delta-overlayLines {
		t.Fatalf("convergence delta inconsistency: got %d want %d-%d", m.ConvergenceDelta, m.Delta, overlayLines)
	}
	wantPass := m.ConvergenceDelta <= m.RequiredMax && m.Connector.Pass
	for _, o := range m.PathOverlays {
		wantPass = wantPass && o.Pass
	}
	if m.Pass != wantPass {
		t.Fatalf("pass flag inconsistency: pass=%v convergence=%+d", m.Pass, m.ConvergenceDelta)
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
		"Billing host composition overlay lines:",
		"Atomic owned resource lifecycle overlay lines:",
		"Keep-warm orchestration overlay lines:",
		"Backend resource pool overlay lines:",
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
		t.Fatalf("Req 11.5 FAIL: raw delta %+d; connector overlay %d/%d; convergence delta %+d (need ≤ %+d)\n%sbaseline_total=%d current_total=%d connector_files=%v",
			m.Delta, m.Connector.Lines, m.Connector.Max, m.ConvergenceDelta, m.RequiredMax,
			b.String(), m.BaselineTotal, m.CurrentTotal, m.Connector.Files)
	}
}
