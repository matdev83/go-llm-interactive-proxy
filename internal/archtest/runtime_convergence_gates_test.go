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
	allow := loadConvergenceAllowlist(t, root)
	got := scanProductionConvergenceGate(t, root, gateHostPath, scanHostPathSource)
	assertConvergenceAllowlistMatch(t, gateHostPath, got, allow)
}

func TestConfigLoad_ProductionAllowlist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	got := scanProductionConvergenceGate(t, root, gateConfigLoad, scanConfigLoadSource)
	assertConvergenceAllowlistMatch(t, gateConfigLoad, got, allow)
}

func TestRuntimeConvergence_AllowlistIntegrity(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	_ = loadConvergenceAllowlist(t, root) // validates schema, gates, classes, duplicates
}
