package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement 8 / stage-five dogfood: keep the canonical stub workflow discoverable and aligned with lipstd.
func TestDogfoodLocalDoc_documentsCLIWorkflowAndExamples(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "dogfood-local.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	for _, needle := range []string{
		"check-config",
		"routes",
		"inventory",
		"serve",
		"dogfood-local-stub.yaml",
		"cmd/lipstd",
		"config/examples",
		"example_configs_test.go",
		"golden_normalize_test.go",
		"testdata",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/dogfood-local.md missing %q", needle)
		}
	}
}

func TestConformanceMatrixEvidence_mentionsMakeParityChecks(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "conformance-matrix-evidence.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	for _, needle := range []string{
		"make parity-checks",
		"-tags=integration",
		"internal/testkit/conformance",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/conformance-matrix-evidence.md missing %q", needle)
		}
	}
}

func TestTestingCoveragePrioritiesDoc_anchorsHotspotsAndCommands(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "testing-coverage-priorities.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	for _, needle := range []string{
		"internal/core/runtime",
		"make parity-checks",
		"spec-bundle-index.md",
		"conformance-matrix-evidence.md",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/testing-coverage-priorities.md missing %q", needle)
		}
	}
}

func TestSpecBundleRoutingScenariosDoc_tracksRegistry(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "spec-bundle-routing-scenarios.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	for _, needle := range []string{
		"SB-ROUTE-parse-primaries",
		"spec_bundle_scenarios.go",
		"internal/core/routing",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/spec-bundle-routing-scenarios.md missing %q", needle)
		}
	}
}
