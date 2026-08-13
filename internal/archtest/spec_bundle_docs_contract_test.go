package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtensionAuthoringDoc_DescribesCurrentArchitecture(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "extension-authoring.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, needle := range []string{
		"Provider profile", "Family adapter", "Executable connector", "pkg/lipsdk/backendplugin/contracttest",
		"1,000", "profile-only", "canonical", "semantic", "sentinel", "fail-closed",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("extension authoring documentation missing %q", needle)
		}
	}
}

func TestArchitectureDoc_DoesNotPresentCartesianReleaseArchitecture(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "<!-- architecture-contract: non-cartesian-release-evidence -->") {
		t.Fatal("architecture.md is missing the stable non-Cartesian architecture contract marker")
	}
}
