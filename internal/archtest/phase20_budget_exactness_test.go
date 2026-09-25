package archtest

import (
	"path/filepath"
	"testing"
)

// Audited remediation-3A baselines: exact live measurements behind the refreshed
// ceilings. Each ceiling must equal its audited baseline + 25 (repository headroom
// convention); the live tree may only shrink below the audit (deletions allowed)
// and must never exceed its ceiling. PR #659 adversarial repair (R1-R10) re-audited
// runtimebundle to 13659 and internal/core to 139708 from the reviewed production
// additions; stdhttp, process_services.go, and the connector overlay are unchanged.
// PR #659 CodeRabbit durable candidate-budget backoff/logging (commit 652b9773)
// re-audited runtimebundle to 13757 (observation_economic_bridge.go); the same
// change leaves stdhttp, internal/core, process_services.go, and the connector
// overlay unchanged. PR #666 adversarial F1-F6 behavior repairs re-audited
// internal/core to 140016 from the reviewed economics production additions;
// runtimebundle, stdhttp, process_services.go, and the connector overlay are
// unchanged. PR #666 adversarial F1 pre-execution rejection re-audited
// runtimebundle to 13817 (generation backend-kind inventory + candidate-local
// V2 binding); internal/core, stdhttp, process_services.go, and the
// connector overlay are unchanged by that F1 guard. PR #666 reviewed N1/N2
// component_rater (+81), N3 provider_evidence (+28), and F4 stream_terminal
// cleanup (+5) production additions re-audited internal/core to 140130;
// runtimebundle, stdhttp, process_services.go, and the
// connector overlay are unchanged by that growth-budget refresh.
const (
	phase20AuditRuntimebundleLines    = 13817
	phase20AuditStdhttpLines          = 7134
	phase20AuditCoreLines             = 140130
	phase20AuditProcessServicesLines  = 342
	phase20AuditConnectorOverlayLines = 2321
	phase20BudgetHeadroom             = 25
	phase20CeilingRuntimebundle       = phase20AuditRuntimebundleLines + phase20BudgetHeadroom
	phase20CeilingStdhttp             = phase20AuditStdhttpLines + phase20BudgetHeadroom
	phase20CeilingCore                = phase20AuditCoreLines + phase20BudgetHeadroom
	phase20CeilingProcessServices     = phase20AuditProcessServicesLines + phase20BudgetHeadroom
	phase20CeilingConnectorOverlay    = phase20AuditConnectorOverlayLines + phase20BudgetHeadroom
)

// TestPhase20RefreshedBudgetHeadroomExact pins the remediation-3A budget refresh
// to its audited arithmetic: every refreshed ceiling equals audited baseline + 25,
// and the live tree stays at or below its ceiling. Budget tests fail on excess;
// this test additionally fails on any inflation of a ceiling beyond audited + 25,
// so the refreshed ceilings cannot silently drift upward. Beneficial deletions
// reduce the live measurement and keep passing.
func TestPhase20RefreshedBudgetHeadroomExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	treeCeiling := func(tree string, audited, ceiling int) {
		t.Helper()
		max := -1
		for _, b := range PackageTreeBudgets {
			if b.Tree == tree {
				max = b.Max
			}
		}
		if max != ceiling {
			t.Fatalf("%s: PackageTreeBudgets Max=%d, want audited %d + 25 = %d", tree, max, audited, ceiling)
		}
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(tree)))
		if err != nil {
			t.Fatal(err)
		}
		if msg := budgetBoundaryError(tree, n, max); msg != "" {
			t.Fatal(msg)
		}
	}
	lineCeiling := func(dir string, audited, ceiling int) {
		t.Helper()
		max := -1
		for _, b := range LineBudgets {
			if b.Dir == dir {
				max = b.Max
			}
		}
		if max != ceiling {
			t.Fatalf("%s: LineBudgets Max=%d, want audited %d + 25 = %d", dir, max, audited, ceiling)
		}
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatal(err)
		}
		if msg := budgetBoundaryError(dir, n, max); msg != "" {
			t.Fatal(msg)
		}
	}

	treeCeiling("internal/infra/runtimebundle", phase20AuditRuntimebundleLines, phase20CeilingRuntimebundle)
	treeCeiling("internal/stdhttp", phase20AuditStdhttpLines, phase20CeilingStdhttp)
	lineCeiling("internal/core", phase20AuditCoreLines, phase20CeilingCore)

	const processServicesPath = "internal/infra/runtimebundle/process_services.go"
	fileMax := -1
	for _, b := range CriticalFileBudgets {
		if b.Path == processServicesPath {
			fileMax = b.Max
		}
	}
	if fileMax != phase20CeilingProcessServices {
		t.Fatalf("%s: CriticalFileBudgets Max=%d, want audited %d + 25 = %d", processServicesPath, fileMax, phase20AuditProcessServicesLines, phase20CeilingProcessServices)
	}
	n, err := CountFileLines(filepath.Join(root, filepath.FromSlash(processServicesPath)))
	if err != nil {
		t.Fatal(err)
	}
	if msg := budgetBoundaryError(processServicesPath, n, fileMax); msg != "" {
		t.Fatal(msg)
	}

	if ConnectorArchitectureOverlayMax != phase20CeilingConnectorOverlay {
		t.Fatalf("ConnectorArchitectureOverlayMax=%d, want audited %d + 25 = %d", ConnectorArchitectureOverlayMax, phase20AuditConnectorOverlayLines, phase20CeilingConnectorOverlay)
	}
	overlay, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	if msg := budgetBoundaryError("connector overlay", overlay.Lines, overlay.Max); msg != "" {
		t.Fatal(msg)
	}
}

// TestPhase20BudgetBoundaryReductionPassesExcessFails is the pure boundary
// regression for the ceiling helper: deep reductions and at-cap measurements pass,
// while ceiling+1 fails.
func TestPhase20BudgetBoundaryReductionPassesExcessFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		measured int
		ceiling  int
		wantErr  bool
	}{
		{name: "deep reduction passes", measured: 0, ceiling: 100, wantErr: false},
		{name: "headroom reduction passes", measured: 74, ceiling: 100, wantErr: false},
		{name: "at cap passes", measured: 100, ceiling: 100, wantErr: false},
		{name: "ceiling plus one fails", measured: 101, ceiling: 100, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := budgetBoundaryError("boundary", tc.measured, tc.ceiling)
			if tc.wantErr && msg == "" {
				t.Fatalf("measured=%d ceiling=%d: want excess error, got none", tc.measured, tc.ceiling)
			}
			if !tc.wantErr && msg != "" {
				t.Fatalf("measured=%d ceiling=%d: want pass, got %q", tc.measured, tc.ceiling, msg)
			}
		})
	}
}
