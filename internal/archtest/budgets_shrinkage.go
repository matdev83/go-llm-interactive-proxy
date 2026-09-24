package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RuntimeConvergenceShrinkageBaselineSHA is the reviewed production baseline for Req 11.5.
const RuntimeConvergenceShrinkageBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// RuntimeConvergenceMinNetLineReduction is the Requirement 11.5 floor for the
// legacy convergence component (after subtracting the ADR 0008 overlay).
const RuntimeConvergenceMinNetLineReduction = 800

// ConnectorArchitectureOverlayMax is the exact-measured ADR 0008 connector
// architecture overlay ratchet (non-test lines in structurally selected files).
// The reliability work adds explicit discovered-plugin artifact ownership and
// cleanup to the connector composition path. Keep 25 lines of ratchet headroom
// over the reviewed 2,275-line overlay.
// Usage-economics reconciliation adds the economic observation bridge to
// process_services.go (+26; the other 9 selected files are byte-identical to the
// merge base); re-measured 2,321-line overlay, reset to 2346 with 25 headroom.
const ConnectorArchitectureOverlayMax = 2346

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

// OverlayMeasurement is one architecture-overlay allowance: the files a feature
// added to the convergence surfaces, their measured non-test lines, the ratchet
// cap, and whether the cap holds.
type OverlayMeasurement struct {
	Name  string
	Files []string
	Lines int
	Max   int
	Pass  bool
}

// ShrinkageMeasurement is the Requirement 11.5 aggregate plus architecture overlays.
type ShrinkageMeasurement struct {
	BaselineSHA      string
	Surfaces         []AffectedSurfaceMeasurement
	BaselineTotal    int
	CurrentTotal     int
	Delta            int // raw current-baseline (includes overlay lines)
	Connector        OverlayMeasurement
	PathOverlays     []OverlayMeasurement
	Growth           OverlayMeasurement // usage-economics growth allowance (per-file growth above locked baselines)
	ConvergenceDelta int                // Delta - overlay lines (legacy Req 11.5 component)
	RequiredMax      int
	Pass             bool
}

// MeasureConnectorArchitectureOverlay counts non-test lines in affected-surface
// production files that import connector discovery/trust/catalog/ABI packages.
func MeasureConnectorArchitectureOverlay(root string) (OverlayMeasurement, error) {
	m, err := measureOverlayByMarkers(root, connectorArchitectureOverlayImportMarkers, ConnectorArchitectureOverlayMax, nil)
	if err != nil {
		return OverlayMeasurement{}, err
	}
	m.Name = "Connector"
	return m, nil
}

func measureOverlayByMarkers(root string, markers []string, maxBytes int, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Max: maxBytes}
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
			return OverlayMeasurement{}, fmt.Errorf("%s: %w", s.Tree, err)
		}
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}

// MeasureRuntimeConvergenceShrinkage compares current lines to the locked baseline
// and separates the architecture overlays from the legacy convergence component.
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
	connector, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.Connector = connector
	pathOverlays, err := measurePathMarkerOverlays(root, connector.Files)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.PathOverlays = pathOverlays
	claimed := make(map[string]struct{}, len(connector.Files))
	for _, f := range connector.Files {
		claimed[f] = struct{}{}
	}
	for _, o := range pathOverlays {
		for _, f := range o.Files {
			claimed[f] = struct{}{}
		}
	}
	growth, err := measureUsageEconomicsGrowthOverlay(root, claimed)
	if err != nil {
		return ShrinkageMeasurement{}, err
	}
	m.Growth = growth
	overlayLines := m.Connector.Lines
	m.Pass = m.Connector.Pass
	for _, o := range m.PathOverlays {
		overlayLines += o.Lines
		m.Pass = m.Pass && o.Pass
	}
	overlayLines += m.Growth.Lines
	m.Pass = m.Pass && m.Growth.Pass
	m.ConvergenceDelta = m.Delta - overlayLines
	m.Pass = m.Pass && m.ConvergenceDelta <= m.RequiredMax
	return m, nil
}

// overlays returns the connector overlay followed by the path-marker overlays in
// table order and the usage-economics growth allowance, for uniform report formatting.
func (m ShrinkageMeasurement) overlays() []OverlayMeasurement {
	out := make([]OverlayMeasurement, 0, 2+len(m.PathOverlays))
	out = append(out, m.Connector)
	out = append(out, m.PathOverlays...)
	out = append(out, m.Growth)
	return out
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
	fmt.Fprintln(&b, "ADR 0008 connector-architecture overlay: approved public host/discovery additions are measured structurally (import markers for discovery/catalog/trust/diagnostics/backendplugin ABI). Additional feature overlays select new production files by path. Each overlay and the legacy convergence component are ratcheted separately.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Surface | Baseline | Current | Delta |")
	fmt.Fprintln(&b, "| --- | ---: | ---: | ---: |")
	for _, s := range m.Surfaces {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %+d |\n", s.Tree, s.BaselineLines, s.CurrentLines, s.Delta)
	}
	fmt.Fprintf(&b, "| **TOTAL** | **%d** | **%d** | **%+d** |\n", m.BaselineTotal, m.CurrentTotal, m.Delta)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Raw delta (includes overlays): `%+d`\n\n", m.Delta)
	for _, o := range m.overlays() {
		fmt.Fprintf(&b, "%s overlay lines: `%d` (cap `%d`; files: `%s`)\n\n", o.Name, o.Lines, o.Max, strings.Join(o.Files, "`, `"))
	}
	fmt.Fprintf(&b, "Convergence delta (raw − overlays): `%+d`\n\n", m.ConvergenceDelta)
	fmt.Fprintf(&b, "Required: convergence delta ≤ %+d (remove ≥ %d lines after overlays)", m.RequiredMax, RuntimeConvergenceMinNetLineReduction)
	for _, o := range m.overlays() {
		fmt.Fprintf(&b, "; %s overlay ≤ %d", strings.ToLower(o.Name), o.Max)
	}
	fmt.Fprintln(&b, ".")
	fmt.Fprintln(&b)
	if m.Pass {
		fmt.Fprintln(&b, "Verdict: **PASS**")
	} else {
		var reasons []string
		if m.ConvergenceDelta > m.RequiredMax {
			reasons = append(reasons, fmt.Sprintf("convergence short by %d lines to reach ≤ %+d", m.ConvergenceDelta-m.RequiredMax, m.RequiredMax))
		}
		for _, o := range m.overlays() {
			if !o.Pass {
				reasons = append(reasons, fmt.Sprintf("%s overlay measured %d exceeds cap %d", strings.ToLower(o.Name), o.Lines, o.Max))
			}
		}
		fmt.Fprintf(&b, "Verdict: **FAIL** (%s).\n", strings.Join(reasons, "; "))
	}
	fmt.Fprintln(&b)
	return b.String(), m, nil
}
