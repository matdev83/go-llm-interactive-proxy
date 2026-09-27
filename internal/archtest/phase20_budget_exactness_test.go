package archtest

import (
	"path/filepath"
	"testing"
)

// Audited remediation-3A baselines: exact live measurements behind the refreshed
// ceilings. Each ceiling must equal its audited baseline + 25 (repository headroom
// convention); the live tree may only shrink below the audit (deletions allowed)
// and must never exceed its ceiling. The internal/core baseline is re-audited
// after the ingress-self-defense pure config compilation (task 1.1): re-measured
// 137621, bump to 137646 with 25 headroom. Task 1.2 admits the core
// ingressdefense kernel package and its single authoritative policy projection
// from core config: re-measured 137824, bump to 137849 with 25 headroom.
// Tasks 2.1 and 2.2 add the bounded sharded adaptive source state: re-measured
// 138172, bump to 138197 with 25 headroom. Tasks 3.1 and 3.2 add the stdhttp
// self-defense driving adapter and the cycle-neutral request-context
// source-address helper: re-measured 7611 (stdhttp), superseded by the tasks 4.1/4.2
// headroom. Tasks 4.1 and 4.2 add the transport-auth self-defense outcome
// observer and the private conservative credential-presence probe under
// internal/stdhttp/auth: re-measured 7789 (stdhttp), superseded by the tasks 5.1/5.2
// headroom. Tasks 5.1 and 5.2 add the process-owned adaptive state, the
// cycle-neutral self-defense security projection with its credential-disposition
// probe, and the standard data-plane gate and auth observation wiring:
// re-measured 13762 (runtimebundle) and 8013 (stdhttp), reset to 13787 and 8038
// with 25 headroom. Tasks 7.1/7.2 and the adaptive-exemption filter on the
// auth-observation path added 8 further production lines to internal/stdhttp, so
// the live tree now measures 8021 and the unchanged 8038 cap leaves 17 lines of
// headroom rather than 25. The cap was deliberately not raised: the next change
// in this tree must re-measure with the harness before it can be relied on.
// The ingress self-defense review follow-up re-audits all three trees for the
// owned-route carve (the carve in the fixed matcher plus the generation-side
// published-route inventory), the admission-ring admission identity, and the
// effective-value state-limit reload classification: re-measured 13858
// (runtimebundle), 8130 (stdhttp) and 138280 (internal/core), each reset to
// audited + 25.
const (
	phase20AuditRuntimebundleLines    = 13858
	phase20AuditStdhttpLines          = 8375
	phase20AuditCoreLines             = 138280
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
