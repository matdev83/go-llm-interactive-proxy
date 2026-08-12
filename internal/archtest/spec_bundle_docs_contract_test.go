package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecBundleIndex_listsScenarioDocs(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "spec-bundle-index.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	for _, needle := range []string{
		"spec-bundle-orchestration-scenarios.md",
		"spec-bundle-continuity-scenarios.md",
		"spec-bundle-routing-scenarios.md",
		"spec-bundle-hook-scenarios.md",
		"spec-bundle-identity-scenarios.md",
		"SpecBundleOrchestrationScenarios",
		"SpecBundleContinuityScenarios",
		"SpecBundleRoutingScenarios",
		"SpecBundleHookScenarios",
		"SpecBundleIdentityScenarios",
		"go test -tags=precommit",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/spec-bundle-index.md missing %q", needle)
		}
	}
}

func TestReleaseGates_linksSpecBundleIndex(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "release-gates.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	if !strings.Contains(text, "spec-bundle-index.md") {
		t.Fatal("docs/release-gates.md should link spec-bundle-index.md for traceability")
	}
}

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

func TestConformanceEvidenceDoc_DoesNotPresentCartesianReleaseArchitecture(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "docs", "conformance-matrix-evidence.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "<!-- architecture-contract: non-cartesian-release-evidence -->") {
		t.Fatal("conformance evidence is missing the stable non-Cartesian architecture contract marker")
	}
}
