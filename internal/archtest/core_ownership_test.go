package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCoreOwnershipManifestCoversEveryTopLevelPackage is the package admission
// gate (Task 11.1, Req 12.3/13.2): every final top-level internal/core/*
// package holding production code must have exactly one ownership manifest
// entry. A new top-level core package without an entry fails this test until
// it receives explicit architecture review.
func TestCoreOwnershipManifestCoversEveryTopLevelPackage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	coreDir := filepath.Join(root, "internal", "core")
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		t.Fatalf("ReadDir internal/core: %v", err)
	}
	byPackage := coreOwnershipByPackage()
	var missing []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "testdata" {
			continue
		}
		rel := filepath.Join("internal", "core", e.Name())
		if !dirHasProductionGo(filepath.Join(root, rel)) {
			continue
		}
		if _, ok := byPackage[e.Name()]; !ok {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("top-level core packages without ownership manifest entry (%d):\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestCoreOwnershipManifestEntriesValid locks the manifest shape: known
// categories only, concise reasons, generic mechanisms carry independent
// consumer evidence, and no stale entries for removed packages.
func TestCoreOwnershipManifestEntriesValid(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	seen := map[string]bool{}
	for _, e := range CoreOwnershipManifest {
		if e.Package == "" {
			t.Errorf("manifest entry with empty package name: %+v", e)
		}
		if seen[e.Package] {
			t.Errorf("duplicate manifest entry for core package %q", e.Package)
		}
		seen[e.Package] = true
		switch e.Category {
		case CoreOwnershipKernelInvariant, CoreOwnershipGenericExtension:
		default:
			t.Errorf("package %q has unknown ownership category %q", e.Package, e.Category)
		}
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("package %q has empty ownership reason", e.Package)
		}
		if e.Category == CoreOwnershipGenericExtension && strings.TrimSpace(e.Consumers) == "" {
			t.Errorf("generic extension mechanism %q must record independent consumer evidence", e.Package)
		}
		dir := filepath.Join(root, "internal", "core", e.Package)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("manifest entry %q does not match a top-level internal/core directory", e.Package)
		}
	}
}

func dirHasProductionGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}
