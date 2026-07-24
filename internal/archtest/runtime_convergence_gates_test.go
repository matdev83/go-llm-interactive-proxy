package archtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const runtimeConvergenceAllowlistRel = "internal/archtest/testdata/architecture/runtime_convergence_allowlist.json"

func loadConvergenceAllowlist(t *testing.T, root string) []convergenceAllowlistEntry {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(runtimeConvergenceAllowlistRel))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	var file convergenceAllowlistFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode allowlist: %v", err)
	}
	if file.SchemaVersion != 1 {
		t.Fatalf("allowlist schema_version=%d, want 1", file.SchemaVersion)
	}
	seen := map[string]bool{}
	var out []convergenceAllowlistEntry
	for i, e := range file.Entries {
		if !knownConvergenceGates[e.Gate] {
			t.Fatalf("allowlist[%d]: unknown gate %q", i, e.Gate)
		}
		if !knownConvergenceClasses[e.Classification] {
			t.Fatalf("allowlist[%d]: unknown classification %q", i, e.Classification)
		}
		if strings.TrimSpace(e.Path) == "" || strings.TrimSpace(e.Identity) == "" {
			t.Fatalf("allowlist[%d]: path and identity required", i)
		}
		if strings.TrimSpace(e.RetirementTask) == "" || strings.TrimSpace(e.Rationale) == "" {
			t.Fatalf("allowlist[%d]: retirement_task and rationale required", i)
		}
		if reason := allowlistEntryViolatesTask44Retirement(e); reason != "" {
			t.Fatalf("allowlist[%d]: %s", i, reason)
		}
		k := e.key()
		if seen[k] {
			t.Fatalf("allowlist duplicate identity %s", k)
		}
		seen[k] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

type convergenceScanner func(filename, src string) ([]convergenceFinding, error)

func scanProductionConvergenceGate(t *testing.T, root, gate string, scan convergenceScanner) []convergenceFinding {
	t.Helper()
	var out []convergenceFinding
	err := walkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		rel = filepath.ToSlash(rel)
		fs, err := scan(rel, string(src))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for _, f := range fs {
			if f.Gate != gate {
				continue
			}
			f.Path = rel
			out = append(out, f)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk production: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func assertConvergenceAllowlistMatch(t *testing.T, gate string, findings []convergenceFinding, allow []convergenceAllowlistEntry) {
	t.Helper()
	bad := convergenceAllowlistDrift(gate, findings, allow)
	if len(bad) != 0 {
		t.Fatalf("%s allowlist drift (%d):\n%s", gate, len(bad), strings.Join(bad, "\n"))
	}
}

func convergenceAllowlistDrift(gate string, findings []convergenceFinding, allow []convergenceAllowlistEntry) []string {
	var bad []string
	want := map[string]convergenceAllowlistEntry{}
	for _, e := range allow {
		if e.Gate != gate {
			continue
		}
		k := e.key()
		if _, exists := want[k]; exists {
			bad = append(bad, "duplicate allowlist entry (one-to-one violated): "+k)
			continue
		}
		want[k] = e
	}
	// Preserve every live finding; never collapse duplicate site identities.
	got := map[string]convergenceFinding{}
	for _, f := range findings {
		k := f.key()
		if _, exists := got[k]; exists {
			bad = append(bad, "duplicate finding identity (site collision): "+f.String())
			continue
		}
		got[k] = f
	}
	for k, f := range got {
		e, ok := want[k]
		if !ok {
			bad = append(bad, "unexpected finding (new legacy growth?): "+f.String())
			continue
		}
		if e.Classification != f.Classification {
			bad = append(bad, fmt.Sprintf("%s: classification got %s want %s", k, f.Classification, e.Classification))
		}
	}
	for k, e := range want {
		if _, ok := got[k]; !ok {
			bad = append(bad, "stale allowlist entry (code removed; delete entry): "+k+" retirement="+e.RetirementTask)
		}
	}
	sort.Strings(bad)
	return bad
}

func TestRuntimeConvergence_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	got := scanProductionConvergenceGate(t, root, gateRuntimeConvergence, scanRuntimeConvergenceSource)
	assertConvergenceAllowlistMatch(t, gateRuntimeConvergence, got, allow)
}

func TestReloadContract_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	got := scanProductionConvergenceGate(t, root, gateReloadContract, scanReloadContractSource)
	assertConvergenceAllowlistMatch(t, gateReloadContract, got, allow)
}

func TestHostPath_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateHostPath, scanHostPathSource)
	assertHostPathExactBuildHostGraph(t, got)
}

func TestConfigLoad_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateConfigLoad, scanConfigLoadSource)
	assertConfigLoadExactCanonicalOwner(t, got)
}

// assertHostPathExactBuildHostGraph requires exactly one BuildHost declaration
// at the canonical path and exactly the two approved production callers.
func assertHostPathExactBuildHostGraph(t *testing.T, got []convergenceFinding) {
	t.Helper()
	var decls, calls, other []convergenceFinding
	for _, f := range got {
		switch {
		case f.Classification == classDeclaration && f.Identity == "func:BuildHost":
			decls = append(decls, f)
		case f.Classification == classCall && strings.Contains(f.Identity, "runtimebundle.BuildHost"):
			calls = append(calls, f)
		default:
			other = append(other, f)
		}
	}
	if len(other) != 0 {
		t.Fatalf("host_path: unexpected findings beyond BuildHost graph:\n%s", formatFindings(other))
	}
	if len(decls) != 1 || decls[0].Path != pathHostBuild {
		t.Fatalf("host_path: want exactly one func:BuildHost at %s, got %v", pathHostBuild, decls)
	}
	wantCalls := map[string]bool{}
	for k := range hostPathAllowedCallKeys {
		wantCalls[k] = true
	}
	gotCalls := map[string]bool{}
	for _, f := range calls {
		gotCalls[f.Path+"|"+f.Identity] = true
	}
	if len(gotCalls) != len(wantCalls) {
		t.Fatalf("host_path: want exactly %d BuildHost callers %v, got %d:\n%s",
			len(wantCalls), wantCalls, len(gotCalls), formatFindings(calls))
	}
	for k := range wantCalls {
		if !gotCalls[k] {
			t.Fatalf("host_path: missing approved caller %s; got:\n%s", k, formatFindings(calls))
		}
	}
}

// assertConfigLoadExactCanonicalOwner requires the canonical owner inventory
// and zero other startup load findings.
func assertConfigLoadExactCanonicalOwner(t *testing.T, got []convergenceFinding) {
	t.Helper()
	want := map[string]string{
		pathBootstrapEffective + "|func:LoadBootstrapEffectiveWithSource":                             classOwner,
		pathBootstrapEffective + "|call:LoadBootstrapEffectiveWithSource->config.LoadEffective#1": classCall,
	}
	gotKeys := map[string]convergenceFinding{}
	for _, f := range got {
		gotKeys[f.Path+"|"+f.Identity] = f
	}
	if len(gotKeys) != len(want) {
		t.Fatalf("config_load: want exact canonical owner inventory (%d entries), got %d:\n%s",
			len(want), len(gotKeys), formatFindings(got))
	}
	for k, class := range want {
		f, ok := gotKeys[k]
		if !ok {
			t.Fatalf("config_load: missing %s; got:\n%s", k, formatFindings(got))
		}
		if f.Classification != class {
			t.Fatalf("config_load: %s classification=%s want %s", k, f.Classification, class)
		}
	}
}

// TestInspectPurity_ProductionAllowlist enforces the Task 5.3 Inspect
// invariant with zero exceptions: CLI routes/inventory drivers and the
// InspectRoutes/InspectInventory/prepareInspect composition-root graph never
// reach a broad bootstrap/host/process/generation-owner symbol (or wrapper).
// There is no allowlist entry for this gate, so any finding fails immediately.
func TestInspectPurity_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateInspectPurity, scanInspectPuritySource)
	assertConvergenceAllowlistMatch(t, gateInspectPurity, got, nil)
}

// TestValidationPurity_ProductionAllowlist enforces the Task 5.4
// ValidateDistribution invariant with zero exceptions: runCheckConfigCommand
// and the focused validate_distribution.go operation graph never reach a
// Manager/publish/host/listener owner (or wrapper). There is no allowlist
// entry for this gate, so any finding fails immediately.
func TestValidationPurity_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateValidationPurity, scanValidationPuritySource)
	assertConvergenceAllowlistMatch(t, gateValidationPurity, got, nil)
}

func TestRuntimeConvergence_AllowlistIntegrity(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_ = loadConvergenceAllowlist(t, root) // validates schema, gates, classes, duplicates
}
