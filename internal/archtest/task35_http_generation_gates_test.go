package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 3.5 production gates: after RequestPlane compatibility deletion these
// must stay green. Phase 4 Built sites are exact allowlist grandfather entries.

func TestTask35_BroadRequestPlane_ProductionForbidden(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateBroadRequestPlane, scanBroadRequestPlaneSource)
	if len(got) > 0 {
		var b strings.Builder
		for _, f := range got {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("Task 3.5: broad runtimebundle.RequestPlane must be deleted (%d findings):\n%s", len(got), b.String())
	}
}

func TestTask35_CompatHTTPSymbols_ProductionForbidden(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateCompatHTTPSymbols, scanCompatHTTPSymbolsSource)
	if len(got) > 0 {
		var b strings.Builder
		for _, f := range got {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("Task 3.5: deleted RequestPlane compatibility symbols must stay gone (%d findings):\n%s", len(got), b.String())
	}
}

func TestTask35_FocusedHTTPLifecycle_ProductionForbidden(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateFocusedHTTPLifecycle, scanFocusedHTTPLifecycleSource)
	if len(got) > 0 {
		var b strings.Builder
		for _, f := range got {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("Task 3.5: focused HTTP composition must stay lifecycle/generic-bag free (%d findings):\n%s", len(got), b.String())
	}
}

func TestTask35_StdhttpBuilt_Phase4GrandfatherOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	got := scanProductionConvergenceGate(t, root, gateStdhttpBuilt, scanStdhttpBuiltSource)
	assertConvergenceAllowlistMatch(t, gateStdhttpBuilt, got, allow)
}

func TestTask35_CanonicalGeneration_NoCloserOrLegacyAggregate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateCanonicalClosers, scanCanonicalGenerationClosersSource)
	if len(got) > 0 {
		var b strings.Builder
		for _, f := range got {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("Task 3.5: canonical generation path must not use closer/legacy aggregates (%d findings):\n%s", len(got), b.String())
	}
}

func TestTask35_CandidateLegacyClosers_Phase4GrandfatherOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allow := loadConvergenceAllowlist(t, root)
	got := scanProductionConvergenceGate(t, root, gateCandidateLegacyClosers, scanCandidateLegacyClosersSource)
	assertConvergenceAllowlistMatch(t, gateCandidateLegacyClosers, got, allow)
}

func TestTask35_ComposeInventory_NoComposeRequestPlane(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	invTest := filepath.Join(root, "internal", "stdhttp", "mount_contract_inventory_test.go")
	raw, err := os.ReadFile(invTest)
	if err != nil {
		t.Fatalf("read inventory test: %v", err)
	}
	if strings.Contains(string(raw), `"ComposeRequestPlane"`) || strings.Contains(string(raw), "Helper: \"ComposeRequestPlane\"") {
		t.Fatal("Task 3.5: mount_contract_inventory_test.go must not inventory ComposeRequestPlane")
	}
	md := filepath.Join(root, ".kiro", "specs", "runtime-architecture-convergence-and-shrinkage", "mount-dependency-inventory.md")
	mdRaw, err := os.ReadFile(md)
	if err != nil {
		t.Fatalf("read inventory md: %v", err)
	}
	// Live composition-root table rows must not list the deleted composer as a root.
	for _, line := range strings.Split(string(mdRaw), "\n") {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "|") {
			continue
		}
		if strings.Contains(trim, "`ComposeRequestPlane`") && !strings.Contains(strings.ToLower(trim), "deleted") {
			t.Fatalf("Task 3.5: mount-dependency-inventory.md must not list ComposeRequestPlane as a live composition root: %s", trim)
		}
	}
}
