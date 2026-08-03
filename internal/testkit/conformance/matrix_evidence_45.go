package conformance

// Authoritative 5×9 = 45-cell compatibility matrix evidence (spec Phase 8,
// Task 8.5 / Requirement 13.5-13.20).
//
// Every cell classifies the seventeen required features with linked scenario IDs
// and repository test artifacts. Outcomes are one of lossless,
// documented_deterministic_projection, rejected_before_network, or out_of_scope
// (with rationale). No cell or feature may be planned, unclassified, or silently
// unlinked. The registry reuses the OpenResponses frontend row (Task 8.3) and
// backend column (Task 8.4) classifications where they overlap and classifies
// the remaining 32 general cells (GeneralMatrixCells) from the executable
// scenario table (matrix_general_scenarios.go): the tagged integration test
// (matrix_general_conformance_test.go) executes every general-cell scenario
// through a real deployment, and the evidence builder derives the exact same
// scenario IDs from that table (no metadata-only scenario IDs).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Matrix45ScenarioPrefix is the shared scenario-ID prefix for matrix cells not
// covered by the row/column prefixes.
const Matrix45ScenarioPrefix = "matrix45"

// matrix45Artifacts lists the repository test sources that exercise the general
// matrix cells (the executable scenario table test) plus the canonical
// pre-network admission behavior and the shared matrix structural checks.
var matrix45Artifacts = []string{
	"internal/testkit/conformance/matrix_test.go",
	"internal/testkit/conformance/matrix_general_conformance_test.go",
	"internal/testkit/conformance/matrix_general_scenarios.go",
	"internal/testkit/conformance/matrix_contract.go",
	"internal/testkit/conformance/openresponses_provider_mode.go",
	"internal/testkit/conformance/connector_host.go",
	"internal/testkit/conformance/connector_wire.go",
	"internal/testkit/conformance/connector_columns_matrix_test.go",
	"internal/core/capabilities/admit.go",
	"internal/core/routing/candidate_admission.go",
}

// Matrix45CellEvidence is one authoritative cell with complete feature evidence.
type Matrix45CellEvidence struct {
	Frontend string
	Backend  string
	Features map[FeatureID]FeatureEvidence
}

func matrix45ScenarioID(frontend, backend, suffix string) string {
	return Matrix45ScenarioPrefix + "-" + frontend + "-" + backend + "-" + suffix
}

func matrix45ArtifactRef() []string {
	return append([]string(nil), matrix45Artifacts...)
}

// Matrix45Evidence returns the authoritative feature classification for all 45
// cells. Row/column overlaps reuse their Task 8.3/8.4 registries verbatim; the
// remaining cells are classified from the all-cells conformance evidence.
func Matrix45Evidence() []Matrix45CellEvidence {
	out := make([]Matrix45CellEvidence, 0, len(AllCells()))
	for _, cell := range AllCells() {
		out = append(out, Matrix45CellEvidence{
			Frontend: cell.Frontend,
			Backend:  cell.Backend,
			Features: matrix45CellFeaturesFor(cell.Frontend, cell.Backend),
		})
	}
	return out
}

func matrix45CellFeaturesFor(frontend, backend string) map[FeatureID]FeatureEvidence {
	if frontend == FrontendOpenResponses {
		row := openResponsesFrontendRowCellFor(backend)
		return row.Features
	}
	if backend == BackendOpenResponses {
		col := openResponsesBackendColumnCellFor(frontend)
		return col.Features
	}
	return matrix45GeneralFeatures(frontend, backend)
}

// ValidateMatrix45Evidence checks that every one of the 45 cells classifies every
// required feature with release-ready, artifact-linked evidence. It returns a
// non-nil error describing the first violation so release gates fail closed.
func ValidateMatrix45Evidence(moduleRoot string) error {
	required := OpenResponsesFrontendRowRequiredFeatures()
	for _, cell := range Matrix45Evidence() {
		for _, feat := range required {
			ev, ok := cell.Features[feat]
			if !ok {
				return fmt.Errorf("matrix cell %s × %s: missing evidence for feature %q", cell.Frontend, cell.Backend, feat)
			}
			if err := ev.ValidateReleaseReady(); err != nil {
				return fmt.Errorf("matrix cell %s × %s feature %q: %w", cell.Frontend, cell.Backend, feat, err)
			}
			if ev.Outcome == OutcomeOutOfScope {
				continue
			}
			for _, art := range ev.TestArtifacts {
				if !matrix45ArtifactExists(moduleRoot, art) {
					return fmt.Errorf("matrix cell %s × %s feature %q: linked test artifact not found: %s", cell.Frontend, cell.Backend, feat, art)
				}
			}
			for _, sid := range ev.ScenarioIDs {
				if !matrix45ScenarioIDValid(cell.Frontend, cell.Backend, sid) {
					return fmt.Errorf("matrix cell %s × %s feature %q: scenario ID %q does not use a known cell scenario prefix", cell.Frontend, cell.Backend, feat, sid)
				}
			}
		}
	}
	return nil
}

func matrix45ArtifactExists(moduleRoot, rel string) bool {
	if strings.TrimSpace(rel) == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(moduleRoot, filepath.FromSlash(rel)))
	return err == nil
}

// Matrix45Scenario is one executable scenario entry for one of the 45 cells.
type Matrix45Scenario struct {
	Frontend   string
	Backend    string
	Feature    FeatureID
	ScenarioID string
}

// Matrix45Scenarios returns the authoritative executable scenario table for all
// 45 cells. It composes the OpenResponses frontend row table (cells whose
// frontend is openresponses), the OpenResponses backend column table (cells
// whose backend is openresponses, excluding the shared overlap cell owned by
// the row), and the general matrix executable table for the remaining 32 cells.
//
// The evidence builder (matrix45CellFeaturesFor) links exactly the scenario IDs
// of this table, and the tagged integration suites execute every entry, so the
// release gate can prove every one of the 45 evidence scenario IDs corresponds
// to an executed scenario with no row/column exemption.
func Matrix45Scenarios() []Matrix45Scenario {
	var out []Matrix45Scenario
	for _, cell := range AllCells() {
		switch {
		case cell.Frontend == FrontendOpenResponses:
			for _, sc := range OpenResponsesFrontendRowScenarios() {
				if sc.Backend != cell.Backend {
					continue
				}
				out = append(out, Matrix45Scenario{
					Frontend:   cell.Frontend,
					Backend:    cell.Backend,
					Feature:    sc.Feature,
					ScenarioID: sc.ScenarioID,
				})
			}
		case cell.Backend == BackendOpenResponses:
			for _, sc := range OpenResponsesBackendColumnScenarios() {
				if sc.Frontend != cell.Frontend {
					continue
				}
				out = append(out, Matrix45Scenario{
					Frontend:   cell.Frontend,
					Backend:    cell.Backend,
					Feature:    sc.Feature,
					ScenarioID: sc.ScenarioID,
				})
			}
		default:
			for _, sc := range GeneralMatrixScenarios() {
				if sc.Frontend != cell.Frontend || sc.Backend != cell.Backend {
					continue
				}
				out = append(out, Matrix45Scenario{
					Frontend:   cell.Frontend,
					Backend:    cell.Backend,
					Feature:    sc.Feature,
					ScenarioID: sc.ScenarioID,
				})
			}
		}
	}
	return out
}
