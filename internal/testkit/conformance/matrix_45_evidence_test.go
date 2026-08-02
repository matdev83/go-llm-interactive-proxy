package conformance_test

import (
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance"
	testkitopenresponses "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/openresponses"
)

// TestMatrix45_EvidenceReleaseReady is the Task 8.5 matrix evidence validator.
// It fails when any of the 45 cells has a missing, planned, unclassified, or
// unlinked feature outcome, or links a scenario ID that is not registered or a
// test artifact that does not exist on disk.
func TestMatrix45_EvidenceReleaseReady(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	if err := conformance.ValidateMatrix45Evidence(root); err != nil {
		t.Fatalf("45-cell matrix evidence is not release-ready: %v", err)
	}
}

// TestMatrix45_AllCellsClassified proves every one of the 45 authoritative cells
// classifies every required feature and carries exactly the cell's driver.
func TestMatrix45_AllCellsClassified(t *testing.T) {
	t.Parallel()
	required := conformance.OpenResponsesFrontendRowRequiredFeatures()
	evidence := conformance.Matrix45Evidence()
	if len(evidence) != 45 {
		t.Fatalf("matrix45 evidence cells = %d, want exactly 45", len(evidence))
	}
	for _, cell := range evidence {
		for _, feat := range required {
			ev, ok := cell.Features[feat]
			if !ok {
				t.Fatalf("cell %s × %s missing feature %q", cell.Frontend, cell.Backend, feat)
			}
			switch ev.Outcome {
			case conformance.OutcomeLossless, conformance.OutcomeProjection,
				conformance.OutcomeRejectBeforeNet, conformance.OutcomeOutOfScope:
			case conformance.OutcomePlanned, conformance.OutcomeUnclassified:
				t.Fatalf("cell %s × %s feature %q is prohibited outcome %q", cell.Frontend, cell.Backend, feat, ev.Outcome)
			default:
				t.Fatalf("cell %s × %s feature %q unknown outcome %q", cell.Frontend, cell.Backend, feat, ev.Outcome)
			}
		}
		drivers := map[string]struct{}{}
		for _, c := range conformance.AllCells() {
			if c.Frontend == cell.Frontend && c.Backend == cell.Backend {
				drivers[string(c.Driver)] = struct{}{}
			}
		}
		if len(drivers) != 1 {
			t.Fatalf("cell %s × %s has %d driver classifications", cell.Frontend, cell.Backend, len(drivers))
		}
	}
}

// TestMatrix45_NoPlannedOrUnclassified forbids the prohibited outcomes at the
// registry level.
func TestMatrix45_NoPlannedOrUnclassified(t *testing.T) {
	t.Parallel()
	seen := map[conformance.CompatibilityOutcome]int{}
	for _, cell := range conformance.Matrix45Evidence() {
		for _, ev := range cell.Features {
			seen[ev.Outcome]++
		}
	}
	if seen[conformance.OutcomePlanned] != 0 {
		t.Fatalf("matrix45 registry contains %d 'planned' outcomes", seen[conformance.OutcomePlanned])
	}
	if seen[conformance.OutcomeUnclassified] != 0 {
		t.Fatalf("matrix45 registry contains %d 'unclassified' outcomes", seen[conformance.OutcomeUnclassified])
	}
}

// TestMatrix45_ScenarioRegistration proves every evidence scenario ID is
// registered in the scenario registry (no unverifiable links).
func TestMatrix45_ScenarioRegistration(t *testing.T) {
	t.Parallel()
	reg := testkitopenresponses.NewScenarioRegistry()
	if err := conformance.RegisterMatrix45Scenarios(reg); err != nil {
		t.Fatalf("register matrix45 scenarios: %v", err)
	}
	for _, cell := range conformance.Matrix45Evidence() {
		for feat, ev := range cell.Features {
			for _, sid := range ev.ScenarioIDs {
				if _, ok := reg.Get(sid); !ok {
					t.Fatalf("matrix45 cell %s × %s feature %q links unregistered scenario %q", cell.Frontend, cell.Backend, feat, sid)
				}
			}
		}
	}
}

// TestMatrix45_MatchesAuthoritativeLists proves the registry is exactly the
// Cartesian product of the authoritative frontend/backend lists.
func TestMatrix45_MatchesAuthoritativeLists(t *testing.T) {
	t.Parallel()
	fe := conformance.BundledFrontendIDs()
	be := conformance.BundledBackendIDs()
	for _, cell := range conformance.Matrix45Evidence() {
		if !slices.Contains(fe, cell.Frontend) || !slices.Contains(be, cell.Backend) {
			t.Fatalf("matrix45 cell %s × %s not in authoritative lists", cell.Frontend, cell.Backend)
		}
	}
}

// TestMatrix45_GeneralCellsExactly32 proves the general matrix cells are exactly
// AllCells minus the OpenResponses row/column: 45 − (9 + 5 − 1) = 32. A reviewer
// note claimed 24; the actual count is pinned here as 32.
func TestMatrix45_GeneralCellsExactly32(t *testing.T) {
	t.Parallel()
	if got := len(conformance.GeneralMatrixCells()); got != 32 {
		t.Fatalf("GeneralMatrixCells() = %d, want exactly 32 (45 minus the 13-cell OpenResponses row/column union)", got)
	}
}

// TestMatrix45_EvidenceScenarioIDsComeFromExecutableTable proves every matrix
// evidence scenario ID is derived from the executable scenario table (no
// metadata-only scenario IDs): the table entries for a cell and the scenario IDs
// linked by that cell's evidence are exactly the same set, and every feature with
// a linked scenario has an executable table entry. Several features share one
// executable scenario (for example instructions/roles and history are proven by
// the json-text round trip), so the comparison is over scenario-ID sets.
//
// The table composes all three authoritative registries — the general matrix
// executable table, the OpenResponses frontend row table, and the OpenResponses
// backend column table — so there is no row/column exemption: all 45 cells must
// link scenario IDs that correspond to executed tables.
func TestMatrix45_EvidenceScenarioIDsComeFromExecutableTable(t *testing.T) {
	t.Parallel()
	tableByCell := map[string]map[string]struct{}{}
	for _, sc := range conformance.Matrix45Scenarios() {
		key := sc.Frontend + "\x00" + sc.Backend
		if tableByCell[key] == nil {
			tableByCell[key] = map[string]struct{}{}
		}
		tableByCell[key][sc.ScenarioID] = struct{}{}
	}
	if len(tableByCell) != 45 {
		t.Fatalf("executable scenario table covers %d cells, want exactly 45", len(tableByCell))
	}
	for _, cell := range conformance.Matrix45Evidence() {
		key := cell.Frontend + "\x00" + cell.Backend
		table := tableByCell[key]
		if len(table) == 0 {
			t.Fatalf("matrix cell %s × %s has no executable table entries", cell.Frontend, cell.Backend)
		}
		linked := map[string]struct{}{}
		for feat, ev := range cell.Features {
			if ev.Outcome == conformance.OutcomeOutOfScope {
				if len(ev.ScenarioIDs) != 0 {
					t.Fatalf("matrix cell %s × %s feature %q is out_of_scope but links scenario IDs", cell.Frontend, cell.Backend, feat)
				}
				continue
			}
			if len(ev.ScenarioIDs) == 0 {
				t.Fatalf("matrix cell %s × %s feature %q has outcome %q but no scenario ID", cell.Frontend, cell.Backend, feat, ev.Outcome)
			}
			for _, sid := range ev.ScenarioIDs {
				if _, ok := table[sid]; !ok {
					t.Fatalf("matrix cell %s × %s feature %q links scenario %q that is not an executable table entry", cell.Frontend, cell.Backend, feat, sid)
				}
				linked[sid] = struct{}{}
			}
		}
		for sid := range table {
			if _, ok := linked[sid]; !ok {
				t.Fatalf("matrix cell %s × %s executable scenario %q is not linked by any evidence feature", cell.Frontend, cell.Backend, sid)
			}
		}
	}
}
