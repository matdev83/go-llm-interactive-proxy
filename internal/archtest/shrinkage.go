package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RuntimeConvergenceShrinkageBaselineSHA is the reviewed production baseline for
// Requirement 11.5 (Task 1.1 / Phase 9.3).
const RuntimeConvergenceShrinkageBaselineSHA = "efe4624909cea318c7211d5cb3734059d3210802"

// RuntimeConvergenceMinNetLineReduction is the Requirement 11.5 floor: the five
// affected surfaces must lose at least this many non-test production lines
// relative to the Phase 1 baseline. Negative deltas are reductions.
const RuntimeConvergenceMinNetLineReduction = 800

// AffectedSurfaceBaseline locks the Phase 1 walk-based non-test line totals
// for one Requirement 11.5 surface. Counts use CountNonTestGoLines (recursive
// physical lines in non-test .go files, including build-tag alternate files).
type AffectedSurfaceBaseline struct {
	Tree          string
	BaselineLines int
}

// RuntimeConvergenceAffectedSurfaces is the Requirement 11.5 inventory. Order
// matches the Phase 1 baseline document and Hermes Phase 9.3 measurement.
//
// Measured at RuntimeConvergenceShrinkageBaselineSHA with CountNonTestGoLines
// (2026-07-25 re-verification matched Hermes: 19642 total).
var RuntimeConvergenceAffectedSurfaces = []AffectedSurfaceBaseline{
	{Tree: "internal/infra/runtimebundle", BaselineLines: 9898},
	{Tree: "internal/infra/runtimehost", BaselineLines: 3056},
	{Tree: "internal/stdhttp", BaselineLines: 4666},
	{Tree: "cmd/lipstd", BaselineLines: 985},
	{Tree: "pkg/lipruntime", BaselineLines: 1037},
}

// AffectedSurfaceMeasurement is one surface's baseline-versus-current delta.
type AffectedSurfaceMeasurement struct {
	Tree          string
	BaselineLines int
	CurrentLines  int
	Delta         int // current - baseline; negative means shrinkage
}

// ShrinkageMeasurement is the Requirement 11.5 aggregate for the five surfaces.
type ShrinkageMeasurement struct {
	BaselineSHA   string
	Surfaces      []AffectedSurfaceMeasurement
	BaselineTotal int
	CurrentTotal  int
	Delta         int // current - baseline; negative means shrinkage
	RequiredMax   int // maximum allowed delta (BaselineTotal-current must be >= 800 ⇒ delta <= -800)
	Pass          bool
}

// MeasureRuntimeConvergenceShrinkage counts current non-test production lines
// for the five affected surfaces and compares them to the locked Phase 1
// baseline. Moving code between packages does not change the verdict helper —
// callers must still reject move-only "shrinkage" under Requirement 11.6.
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
	m.Pass = m.Delta <= m.RequiredMax
	return m, nil
}

// FormatRuntimeConvergenceShrinkage renders the machine-checkable Markdown
// section consumed by make arch-report and Task 9.3 tests.
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
	fmt.Fprintln(&b, "| Surface | Baseline | Current | Delta |")
	fmt.Fprintln(&b, "| --- | ---: | ---: | ---: |")
	for _, s := range m.Surfaces {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %+d |\n", s.Tree, s.BaselineLines, s.CurrentLines, s.Delta)
	}
	fmt.Fprintf(&b, "| **TOTAL** | **%d** | **%d** | **%+d** |\n", m.BaselineTotal, m.CurrentTotal, m.Delta)
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Required: delta ≤ %+d (remove ≥ %d lines).\n\n", m.RequiredMax, RuntimeConvergenceMinNetLineReduction)
	if m.Pass {
		fmt.Fprintln(&b, "Verdict: **PASS**")
	} else {
		need := m.Delta - m.RequiredMax
		fmt.Fprintf(&b, "Verdict: **FAIL** (short by %d lines to reach ≤ %+d).\n", need, m.RequiredMax)
	}
	fmt.Fprintln(&b)
	return b.String(), m, nil
}
