package archtest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CriticalFileBudget caps the non-test line count for hotspot files.
type CriticalFileBudget struct {
	Path string
	Max  int
}

// CriticalFileBudgets is the single source of truth for hotspot ceilings
// (guardrails tests + make arch-report). Values are measured ratchets + 25 lines headroom.
var CriticalFileBudgets = []CriticalFileBudget{
	{Path: "internal/core/runtime/executor.go", Max: 249},
	{Path: "internal/infra/runtimebundle/options.go", Max: 253},
	{Path: "internal/standardplugins/standard_table.go", Max: 211},
	{Path: "internal/pluginreg/reg.go", Max: 372},
	{Path: "internal/stdhttp/server.go", Max: 33},
	{Path: "internal/infra/runtimehost/coordinator.go", Max: 317},
	{Path: "internal/infra/runtimehost/generation.go", Max: 341},
	{Path: "internal/infra/runtimebundle/candidate_compile.go", Max: 284},
	{Path: "internal/infra/runtimebundle/handler_composer.go", Max: 50},
	{Path: "internal/infra/runtimebundle/compile_generation.go", Max: 350},
	{Path: "internal/stdhttp/request_plane.go", Max: 90},
	{Path: "internal/infra/runtimebundle/process_services.go", Max: 290},
	{Path: "pkg/lipruntime/build.go", Max: 121},
	{Path: "pkg/lipruntime/host.go", Max: 93},
	{Path: "pkg/lipruntime/facade.go", Max: 97},
	{Path: "cmd/lipstd/command.go", Max: 458},
	{Path: "pkg/lipruntime/reload.go", Max: 114},
	{Path: "pkg/lipruntime/reload_aliases.go", Max: 60},
}

// PackageTreeBudget caps recursive non-test .go lines for a package tree.
type PackageTreeBudget struct {
	Tree string
	Max  int
}

// PackageTreeBudgets locks measured convergence tree ceilings (+25 lines headroom).
var PackageTreeBudgets = []PackageTreeBudget{
	{Tree: "internal/infra/runtimebundle", Max: 11235},
	{Tree: "internal/stdhttp", Max: 5705},
	{Tree: "cmd/lipstd", Max: 979},
	{Tree: "pkg/lipruntime", Max: 562},
}

// LineBudget caps recursive non-test lines for broader architectural layers.
type LineBudget struct {
	Dir string
	Max int
}

// LineBudgets covers core/pluginreg plus the convergence trees (kept in sync
// with PackageTreeBudgets for overlapping entries).
var LineBudgets = []LineBudget{
	// Routing-override admin, billing host composition, and tool-call
	// classification. Keep the measured-plus-25 ratchet.
	{Dir: "internal/core", Max: 75019},
	{Dir: "internal/pluginreg", Max: 1079},
	{Dir: "internal/stdhttp", Max: 5705},
	{Dir: "internal/infra/runtimebundle", Max: 11235},
	{Dir: "cmd/lipstd", Max: 979},
	{Dir: "pkg/lipruntime", Max: 562},
}

// CountNonTestGoLines recursively counts physical lines in non-test .go files.
func CountNonTestGoLines(dir string) (int, error) {
	var total int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, err := countTreeFileLines(path)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	return total, err
}

func countTreeFileLines(path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// CountFileLines counts physical lines in one file.
func CountFileLines(path string) (int, error) {
	return countTreeFileLines(path)
}

// FormatRuntimeConvergencePackageBudgets renders the advisory Markdown section.
func FormatRuntimeConvergencePackageBudgets(root string) (string, error) {
	var b strings.Builder
	fmt.Fprintln(&b, "## Runtime-convergence package budgets")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Tree | Non-test lines | Budget |")
	fmt.Fprintln(&b, "| --- | --- | --- |")
	for _, budget := range PackageTreeBudgets {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(budget.Tree)))
		if err != nil {
			fmt.Fprintf(&b, "| `%s` | (missing: %v) | %d |\n", budget.Tree, err, budget.Max)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %d |\n", budget.Tree, n, budget.Max)
	}
	fmt.Fprintln(&b)
	return b.String(), nil
}

// RuntimeConvergenceShrinkageBaselineSHA is the reviewed production baseline for Req 11.5.
const RuntimeConvergenceShrinkageBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// RuntimeConvergenceMinNetLineReduction is the Requirement 11.5 floor for the
// legacy convergence component (after subtracting the ADR 0008 overlay).
const RuntimeConvergenceMinNetLineReduction = 800

// ConnectorArchitectureOverlayMax is the exact-measured ADR 0008 connector
// architecture overlay ratchet (non-test lines in structurally selected files).
// The reliability work adds explicit discovered-plugin artifact ownership and
// cleanup to the connector composition path. Keep 25 lines of ratchet headroom
// over the reviewed 1,912-line overlay.
const ConnectorArchitectureOverlayMax = 1937

// GenericCompatibleBackendOverlayMax is the measured generic-compatible-backend-modes
// overlay ratchet (production files selected by path, excluding connector overlay).
// Keep 25 lines of ratchet headroom over the measured 921-line overlay.
const GenericCompatibleBackendOverlayMax = 946

// BillingHostCompositionOverlayMax is the measured billing-host-composition overlay
// ratchet (production files selected by path, excluding connector and generic-compatible
// overlays). Keep 25 lines of ratchet headroom over the measured 310-line overlay.
const BillingHostCompositionOverlayMax = 335

// AffectedSurfaceBaseline locks one Req 11.5 surface baseline.
type AffectedSurfaceBaseline struct {
	Tree          string
	BaselineLines int
}

// RuntimeConvergenceAffectedSurfaces is the Requirement 11.5 inventory.
var RuntimeConvergenceAffectedSurfaces = []AffectedSurfaceBaseline{
	{Tree: "internal/infra/runtimebundle", BaselineLines: 9898},
	{Tree: "internal/infra/runtimehost", BaselineLines: 3056},
	{Tree: "internal/stdhttp", BaselineLines: 4666},
	{Tree: "cmd/lipstd", BaselineLines: 985},
	{Tree: "pkg/lipruntime", BaselineLines: 1037},
}

// connectorArchitectureOverlayImportMarkers selects ADR 0008 host/discovery
// production files by import graph (no maintained connector kind lists).
var connectorArchitectureOverlayImportMarkers = []string{
	"/backendplugins/discovery",
	"/backendplugins/catalog",
	"/backendplugins/trust",
	"/backendplugins/diagnostics",
	"/lipsdk/backendplugin",
}

// AffectedSurfaceMeasurement is one surface's baseline-versus-current delta.
type AffectedSurfaceMeasurement struct {
	Tree          string
	BaselineLines int
	CurrentLines  int
	Delta         int
}

var genericCompatibleBackendOverlayPathMarkers = []string{
	"/core/concurrencyauthority/compatible/",
	"/compatible_admission.go",
	"/compatible_ownership.go",
	"/validate_structural.go",
	"/inventory_live.go",
	"/compatible_admission_limits.go",
}

// billingHostCompositionOverlayPathMarkers selects the new production files the
// billing-host-composition feature adds to the convergence surfaces.
var billingHostCompositionOverlayPathMarkers = []string{
	"/billing_compose.go",
	"/admin/billing/commands.go",
}

// ConnectorOverlayMeasurement is the ADR 0008 connector-architecture allowance.
type ConnectorOverlayMeasurement struct {
	Files []string
	Lines int
	Max   int
	Pass  bool
}

// GenericCompatibleOverlayMeasurement is the generic-compatible-backend-modes allowance.
type GenericCompatibleOverlayMeasurement struct {
	Files []string
	Lines int
	Max   int
	Pass  bool
}

// BillingHostCompositionOverlayMeasurement is the billing-host-composition allowance.
type BillingHostCompositionOverlayMeasurement struct {
	Files []string
	Lines int
	Max   int
	Pass  bool
}

// ShrinkageMeasurement is the Requirement 11.5 aggregate plus architecture overlays.
type ShrinkageMeasurement struct {
	BaselineSHA                   string
	Surfaces                      []AffectedSurfaceMeasurement
	BaselineTotal                 int
	CurrentTotal                  int
	Delta                         int // raw current-baseline (includes overlay lines)
	Overlay                       ConnectorOverlayMeasurement
	GenericCompatibleOverlay      GenericCompatibleOverlayMeasurement
	BillingHostCompositionOverlay BillingHostCompositionOverlayMeasurement
	ConvergenceDelta              int // Delta - overlay lines (legacy Req 11.5 component)
	RequiredMax                   int
	Pass                          bool
}

// MeasureConnectorArchitectureOverlay counts non-test lines in affected-surface
// production files that import connector discovery/trust/catalog/ABI packages.
func MeasureConnectorArchitectureOverlay(root string) (ConnectorOverlayMeasurement, error) {
	return measureOverlayByMarkers(root, connectorArchitectureOverlayImportMarkers, ConnectorArchitectureOverlayMax, nil)
}

// MeasureGenericCompatibleBackendOverlay counts non-test lines for the
// generic-compatible-backend-modes feature, excluding connector overlay files.
func MeasureGenericCompatibleBackendOverlay(root string, exclude map[string]struct{}) (GenericCompatibleOverlayMeasurement, error) {
	base, err := measureOverlayByPathMarkers(root, genericCompatibleBackendOverlayPathMarkers, GenericCompatibleBackendOverlayMax, exclude)
	if err != nil {
		return GenericCompatibleOverlayMeasurement{}, err
	}
	return GenericCompatibleOverlayMeasurement(base), nil
}

// MeasureBillingHostCompositionOverlay counts non-test lines for the
// billing-host-composition feature, excluding connector and generic-compatible
// overlay files.
func MeasureBillingHostCompositionOverlay(root string, exclude map[string]struct{}) (BillingHostCompositionOverlayMeasurement, error) {
	base, err := measureOverlayByPathMarkers(root, billingHostCompositionOverlayPathMarkers, BillingHostCompositionOverlayMax, exclude)
	if err != nil {
		return BillingHostCompositionOverlayMeasurement{}, err
	}
	return BillingHostCompositionOverlayMeasurement(base), nil
}

func measureOverlayByPathMarkers(root string, pathMarkers []string, maxBytes int, exclude map[string]struct{}) (ConnectorOverlayMeasurement, error) {
	m := ConnectorOverlayMeasurement{Max: maxBytes}
	// Preserve the root-wide metric semantics, but do not descend into sibling
	// worktrees. The main checkout may contain hundreds of megabytes of agent
	// worktrees here.
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".worktrees" {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if exclude != nil {
			if _, skip := exclude[rel]; skip {
				return nil
			}
		}
		hit := false
		for _, marker := range pathMarkers {
			if strings.Contains(rel, marker) {
				hit = true
				break
			}
		}
		if !hit {
			return nil
		}
		n, err := countTreeFileLines(path)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, rel)
		m.Lines += n
		return nil
	})
	if err != nil {
		return ConnectorOverlayMeasurement{}, err
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}

func measureOverlayByMarkers(root string, markers []string, maxBytes int, exclude map[string]struct{}) (ConnectorOverlayMeasurement, error) {
	m := ConnectorOverlayMeasurement{Max: maxBytes}
	for _, s := range RuntimeConvergenceAffectedSurfaces {
		dir := filepath.Join(root, filepath.FromSlash(s.Tree))
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if exclude != nil {
				if _, skip := exclude[rel]; skip {
					return nil
				}
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(src)
			hit := false
			for _, marker := range markers {
				if strings.Contains(text, marker) {
					hit = true
					break
				}
			}
			if !hit {
				return nil
			}
			n, err := countTreeFileLines(path)
			if err != nil {
				return err
			}
			m.Files = append(m.Files, rel)
			m.Lines += n
			return nil
		})
		if err != nil {
			return ConnectorOverlayMeasurement{}, fmt.Errorf("%s: %w", s.Tree, err)
		}
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}

// MeasureRuntimeConvergenceShrinkage compares current lines to the locked baseline
// and separates the ADR 0008 connector-architecture overlay from legacy convergence.
func MeasureRuntimeConvergenceShrinkage(root string) (ShrinkageMeasurement, error) {
	m := ShrinkageMeasurement{
		BaselineSHA: RuntimeConvergenceShrinkageBaselineSHA,
		Surfaces:    make([]AffectedSurfaceMeasurement, 0, len(RuntimeConvergenceAffectedSurfaces)),
		RequiredMax: -RuntimeConvergenceMinNetLineReduction,
	}
	for _, s := range RuntimeConvergenceAffectedSurfaces {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(s.Tree)))
		if err != nil {
			return ShrinkageMeasurement{}, fmt.Errorf("%s: %w", s.Tree, err)
		}
		delta := n - s.BaselineLines
		m.Surfaces = append(m.Surfaces, AffectedSurfaceMeasurement{
			Tree:          s.Tree,
			BaselineLines: s.BaselineLines,
			CurrentLines:  n,
			Delta:         delta,
		})
		m.BaselineTotal += s.BaselineLines
		m.CurrentTotal += n
		m.Delta += delta
	}
	overlay, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.Overlay = overlay
	exclude := make(map[string]struct{}, len(overlay.Files))
	for _, f := range overlay.Files {
		exclude[f] = struct{}{}
	}
	genericOverlay, err := MeasureGenericCompatibleBackendOverlay(root, exclude)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.GenericCompatibleOverlay = genericOverlay
	for _, f := range genericOverlay.Files {
		exclude[f] = struct{}{}
	}
	billingOverlay, err := MeasureBillingHostCompositionOverlay(root, exclude)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.BillingHostCompositionOverlay = billingOverlay
	m.ConvergenceDelta = m.Delta - m.Overlay.Lines - m.GenericCompatibleOverlay.Lines - m.BillingHostCompositionOverlay.Lines
	m.Pass = m.ConvergenceDelta <= m.RequiredMax && m.Overlay.Pass && m.GenericCompatibleOverlay.Pass && m.BillingHostCompositionOverlay.Pass
	return m, nil
}

// FormatRuntimeConvergenceShrinkage renders the machine-checkable Markdown section.
func FormatRuntimeConvergenceShrinkage(root string) (string, ShrinkageMeasurement, error) {
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		return "", ShrinkageMeasurement{}, err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "## Runtime-convergence net shrinkage (Req 11.5)")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Baseline SHA: `%s`\n\n", m.BaselineSHA)
	fmt.Fprintln(&b, "Method: recursive `CountNonTestGoLines` (non-test `.go` physical lines, including build-tag alternates). Moving unchanged logic between packages is not shrinkage (Req 11.6).")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "ADR 0008 connector-architecture overlay: approved public host/discovery additions are measured structurally (import markers for discovery/catalog/trust/diagnostics/backendplugin ABI) and excluded from the legacy Req 11.5 convergence delta. Both components are ratcheted separately.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Surface | Baseline | Current | Delta |")
	fmt.Fprintln(&b, "| --- | ---: | ---: | ---: |")
	for _, s := range m.Surfaces {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %+d |\n", s.Tree, s.BaselineLines, s.CurrentLines, s.Delta)
	}
	fmt.Fprintf(&b, "| **TOTAL** | **%d** | **%d** | **%+d** |\n", m.BaselineTotal, m.CurrentTotal, m.Delta)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Raw delta (includes connector overlay): `%+d`\n\n", m.Delta)
	fmt.Fprintf(&b, "Connector overlay lines: `%d` (cap `%d`; files: `%s`)\n\n", m.Overlay.Lines, m.Overlay.Max, strings.Join(m.Overlay.Files, "`, `"))
	fmt.Fprintf(&b, "Generic compatible overlay lines: `%d` (cap `%d`; files: `%s`)\n\n", m.GenericCompatibleOverlay.Lines, m.GenericCompatibleOverlay.Max, strings.Join(m.GenericCompatibleOverlay.Files, "`, `"))
	fmt.Fprintf(&b, "Billing host composition overlay lines: `%d` (cap `%d`; files: `%s`)\n\n", m.BillingHostCompositionOverlay.Lines, m.BillingHostCompositionOverlay.Max, strings.Join(m.BillingHostCompositionOverlay.Files, "`, `"))
	fmt.Fprintf(&b, "Convergence delta (raw − overlays): `%+d`\n\n", m.ConvergenceDelta)
	fmt.Fprintf(&b, "Required: convergence delta ≤ %+d (remove ≥ %d lines after overlays); connector overlay ≤ %d; generic compatible overlay ≤ %d; billing host composition overlay ≤ %d.\n\n", m.RequiredMax, RuntimeConvergenceMinNetLineReduction, ConnectorArchitectureOverlayMax, GenericCompatibleBackendOverlayMax, BillingHostCompositionOverlayMax)
	if m.Pass {
		fmt.Fprintln(&b, "Verdict: **PASS**")
	} else {
		var reasons []string
		if m.ConvergenceDelta > m.RequiredMax {
			reasons = append(reasons, fmt.Sprintf("convergence short by %d lines to reach ≤ %+d", m.ConvergenceDelta-m.RequiredMax, m.RequiredMax))
		}
		if !m.Overlay.Pass {
			reasons = append(reasons, fmt.Sprintf("connector overlay measured %d exceeds cap %d", m.Overlay.Lines, m.Overlay.Max))
		}
		if !m.GenericCompatibleOverlay.Pass {
			reasons = append(reasons, fmt.Sprintf("generic compatible overlay measured %d exceeds cap %d", m.GenericCompatibleOverlay.Lines, m.GenericCompatibleOverlay.Max))
		}
		if !m.BillingHostCompositionOverlay.Pass {
			reasons = append(reasons, fmt.Sprintf("billing host composition overlay measured %d exceeds cap %d", m.BillingHostCompositionOverlay.Lines, m.BillingHostCompositionOverlay.Max))
		}
		fmt.Fprintf(&b, "Verdict: **FAIL** (%s).\n", strings.Join(reasons, "; "))
	}
	fmt.Fprintln(&b)
	return b.String(), m, nil
}
