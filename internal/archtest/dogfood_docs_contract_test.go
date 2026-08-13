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
