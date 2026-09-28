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
// cleanup (+5) production additions re-audited internal/core to 140130.
// PR #666 5B-1/2/3 settlement repair adds 172 verified component_rater
// production lines, re-auditing internal/core to 140302; runtimebundle,
// stdhttp, process_services.go, and the connector overlay are unchanged by
// that growth-budget refresh.
// PR #666 P1-A/P1-B rater fix adds 52 verified component_rater production
// lines, re-auditing internal/core to 140354; runtimebundle, stdhttp,
// process_services.go, and the connector overlay are unchanged by that
// growth-budget refresh.
// PR #666 f356 P1-1/P1-2/P2 partition repair splits the frozen
// component-schema inclusion/partition state machine out of component_rater.go
// into the new component_rater_partition.go and adds the tri-state cover
// resolution, the recursive least-fixpoint cover proof, and the fourth
// (incomplete) partition classification: 321 verified production lines,
// re-auditing internal/core to 140675; runtimebundle, stdhttp,
// process_services.go, and the connector overlay are unchanged by that
// growth-budget refresh.
// PR #666 62a follow-up adds the post-pricing commercial-relevance gate for
// incomplete partitions, the frozen subset quantity-consistency proof, and the
// unobserved-parent fail-closed cover denial: 276 more verified production
// lines, re-auditing internal/core to 140951; runtimebundle, stdhttp,
// process_services.go, and the connector overlay are unchanged by that
// growth-budget refresh.
// PR #666 63c follow-up replaces the remaining direct-only checks with one
// bounded, scope-aware inclusion graph, so an unprovable cover's commercial
// relevance follows recursively represented payable descendants and subset
// quantity consistency follows transitive subset ancestry: 102 more verified
// production lines, re-auditing internal/core to 141095; runtimebundle, stdhttp,
// process_services.go, and the connector overlay are unchanged by that
// growth-budget refresh.
// PR #659 adversarial repair follow-up (review 65) replaces the per-call,
// string-keyed partial containment comparison with one compiled schema program
// plus one interval-constraint solver, adding verified production lines and
// re-auditing internal/core to 141730; runtimebundle, stdhttp,
// process_services.go, and the connector overlay are unchanged by that
// growth-budget refresh.
// PR #659 adversarial solver repair removes the too-strong exact-representation
// gate on the subset upper bound and the presence gate on the complete-coverage
// upper bound: 21 more verified production lines, re-auditing internal/core to
// 141751; the other budgets are unchanged.
// PR #659 adversarial commercial-dependency closure adds the RULE-DERIVED
// dependency predicate beside the amount-based payable set, the per-scope
// hidden-dependency ledger, and the two fail-closed unions that consume them:
// 346 more verified production lines, re-auditing internal/core to 142097; the
// other budgets are unchanged.
// PR #659 adversarial cover-authority consolidation collapses the three
// independent readings of one physical fact into the single
// resolveCompleteCovers authority, names the arithmetic, proof and ownership
// verdicts separately, moves the conflict-suppression set onto the same
// recursive contributor resolution, extracts the one projection of the compiled
// complete-coverage adjacency both consumers walk, and documents the authority
// contract at the definitions: 273 more verified production lines, re-auditing
// internal/core to 142370; the other budgets are unchanged.
// PR #659 adversarial repair 3A splits the 2,798-line component_rater_partition.go
// by concern into component_rater_schema_program.go, component_rater_quantity_solver.go,
// component_rater_cover.go and component_rater_overlap.go. The move is PURE, but a
// file split is not line-neutral: four files carry four package clauses, four import
// blocks, four file headers and their blank-line separators, so the frozen
// state machine measures 2,858 rather than 2,798, +60 verified production lines that
// buy no behaviour, re-auditing internal/core to 142449 (bump to 142474 with 25
// headroom). The per-file growth ceiling is what actually unblocked the split: the
// quantity solver the next planned change targets now carries 437 audited lines
// instead of sharing a 2,784-line ceiling with the whole machine. The other budgets
// are unchanged.
const (
	phase20AuditRuntimebundleLines    = 13817
	phase20AuditStdhttpLines          = 7134
	phase20AuditCoreLines             = 142750
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
