package main

import (
	"path/filepath"
	"testing"
)

//nolint:paralleltest // reads module source tree; keep serial for stable go list
func TestListMatchingTests_LocalstubConformance(t *testing.T) {
	root := repoRoot(t)
	mod := filepath.Join(root, "connectors", "localstub")
	names, err := listMatchingTests(mod, conformanceNameRe)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names {
		if n == "TestConformance_ServiceSuite" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected TestConformance_ServiceSuite, got %v", names)
	}
}

//nolint:paralleltest // reads module source tree; keep serial for stable go list
func TestListMatchingTests_CodexHasParity(t *testing.T) {
	root := repoRoot(t)
	mod := filepath.Join(root, "connectors", "codex")
	names, err := listMatchingTests(mod, conformanceNameRe)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("codex must discover advertised-capability tests")
	}
}

//nolint:paralleltest // reads module source tree; keep serial for stable go list
func TestValidateSelectors_Root(t *testing.T) {
	if err := validateSelectors(repoRoot(t)); err != nil {
		t.Fatal(err)
	}
}
