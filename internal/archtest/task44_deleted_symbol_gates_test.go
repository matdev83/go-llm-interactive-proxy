package archtest

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Task 4.4 production acceptance: deleted Phase 4 symbols and equivalent
// compatibility directions are permanently forbidden; package budgets match
// measured post-deletion sizes; Phase 4 allowlist exceptions are gone.

func TestDeletedSymbol_ProductionForbidden(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var all []convergenceFinding
	for _, s := range deletedSymbolScanners {
		got := scanProductionConvergenceGate(t, root, s.Gate, s.Scan)
		all = append(all, got...)
	}
	if len(all) > 0 {
		t.Fatalf("Task 4.4: deleted symbols / equivalent compatibility directions must stay gone (%d findings):\n%s",
			len(all), formatFindings(all))
	}
}

func TestDeletedSymbol_AllowlistHasNoPhase4OrEarlier(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	bad := validateAllowlistTask44Retirement(allow)
	if len(bad) > 0 {
		t.Fatalf("Task 4.4: Phase 4/legacy allowlist entries must be removed:\n%s", strings.Join(bad, "\n"))
	}
	for _, e := range allow {
		if e.Gate != gateHostPath && e.Gate != gateConfigLoad {
			t.Fatalf("Task 4.4: only Phase 5 host_path/config_load exceptions may remain; got gate %q (%s)", e.Gate, e.key())
		}
		if retirementTaskAtMost(e.RetirementTask, task44AllowlistRetirementFloor) {
			t.Fatalf("Task 4.4: retirement_task %q must be after %s (%s)", e.RetirementTask, task44AllowlistRetirementFloor, e.key())
		}
	}
}

func TestDeletedSymbol_Phase5AllowlistEntriesRemainExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	host := scanProductionConvergenceGate(t, root, gateHostPath, scanHostPathSource)
	assertConvergenceAllowlistMatch(t, gateHostPath, host, allow)
	cfg := scanProductionConvergenceGate(t, root, gateConfigLoad, scanConfigLoadSource)
	assertConvergenceAllowlistMatch(t, gateConfigLoad, cfg, allow)
	if len(allow) == 0 {
		t.Fatal("Task 4.4: Phase 5 host_path/config_load exceptions must remain until named Phase 5 tasks retire them")
	}
}

func TestDeletedSymbol_PackageBudgetsExactMeasured(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if len(PackageTreeBudgets) != 2 {
		t.Fatalf("PackageTreeBudgets: want exactly runtimebundle+stdhttp entries, got %d", len(PackageTreeBudgets))
	}
	for _, tc := range PackageTreeBudgets {
		t.Run(tc.Tree, func(t *testing.T) {
			t.Parallel()
			n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(tc.Tree)))
			if err != nil {
				t.Fatal(err)
			}
			if n != tc.Max {
				t.Fatalf("%s: measured %d non-test lines, want exact budget %d (no headroom)", tc.Tree, n, tc.Max)
			}
			found := false
			for _, b := range lineBudgets {
				if b.dir == tc.Tree {
					found = true
					if b.max != tc.Max {
						t.Fatalf("%s: lineBudgets max=%d must equal PackageTreeBudgets Max=%d", tc.Tree, b.max, tc.Max)
					}
				}
			}
			if !found {
				t.Fatalf("%s missing from lineBudgets (cross-check against PackageTreeBudgets)", tc.Tree)
			}
		})
	}
	t.Run("internal/stdhttp/server.go", func(t *testing.T) {
		t.Parallel()
		want := 8
		n, err := countFileLines(filepath.Join(root, "internal/stdhttp/server.go"))
		if err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("server.go: measured %d lines, want exact %d", n, want)
		}
		found := false
		for _, b := range CriticalFileBudgets {
			if b.Path == "internal/stdhttp/server.go" {
				found = true
				if b.Max != want {
					t.Fatalf("server.go CriticalFileBudgets Max=%d, want %d", b.Max, want)
				}
			}
		}
		if !found {
			t.Fatal("server.go missing from CriticalFileBudgets")
		}
	})
}

func TestDeletedSymbol_PackageTreeBudgetsReportSection(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	section, err := FormatRuntimeConvergencePackageBudgets(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "## Runtime-convergence package budgets") {
		t.Fatalf("report section missing heading:\n%s", section)
	}
	if !strings.Contains(section, "| Tree | Non-test lines | Budget |") {
		t.Fatalf("report section missing table header:\n%s", section)
	}
	for _, b := range PackageTreeBudgets {
		wantRow := "| `" + b.Tree + "` | " + strconv.Itoa(b.Max) + " | " + strconv.Itoa(b.Max) + " |"
		if !strings.Contains(section, wantRow) {
			t.Fatalf("want exact measured=budget row %q in:\n%s", wantRow, section)
		}
	}
}

func TestDeletedSymbol_NoTransitionalExclusionsWeakenStrictGates(t *testing.T) {
	t.Parallel()
	if len(mountHelpersTransitionalAdapters) != 0 {
		t.Fatalf("Task 4.4: mountHelpersTransitionalAdapters must be empty after Phase 4 deletion; got %v", mountHelpersTransitionalAdapters)
	}
	// Former scheduled producer paths must be scanned (no empty-map indirection).
	got, err := scanTask41BuiltCarrierSource("internal/infra/runtimebundle/built.go", `package runtimebundle
func TakeBuilt(b *Built) {}
`)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentity(got, "func:TakeBuilt") {
		t.Fatalf("Task 4.4: former scheduled Built producer path must not be exempt, got %#v", got)
	}
	buildCall, err := scanTask41BuildCallSource("internal/infra/runtimebundle/build.go", `package runtimebundle
func CallCompat() { _, _ = Build(nil, nil, nil, nil) }
`)
	if err != nil {
		t.Fatal(err)
	}
	if !findingsContainIdentityPrefix(buildCall, "call:Build@") {
		t.Fatalf("Task 4.4: former scheduled Build producer path must not be exempt, got %#v", buildCall)
	}
}
