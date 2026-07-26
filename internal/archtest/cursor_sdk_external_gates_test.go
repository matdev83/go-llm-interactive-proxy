package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cursor SDK product code is not implemented yet; these gates keep the root tree
// honest while Task 8.3 locks the external-connector specification.

func TestCursorSDK_forbiddenRootBackendPackageAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "plugins", "backends", "cursorsdk")
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("forbidden root path must not exist: %s", path)
	}
}

func TestCursorSDK_rootGoModHasNoCursorsdkModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{
		"connectors/cursorsdk",
		"/connectors/cursorsdk",
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("root go.mod must not reference %q", bad)
		}
	}
}

func TestCursorSDK_rootPackageJSONHasNoCursorSDK(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "package.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if strings.Contains(string(b), "@cursor/sdk") {
		t.Fatal("root package.json must not depend on @cursor/sdk")
	}
}

func TestCursorSDK_specArtifactsPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, ".kiro", "specs", "cursor-sdk-backend")
	for _, name := range []string{
		"AGENTS.md",
		"requirements.md",
		"design.md",
		"tasks.md",
		"research.md",
		"spec.json",
		"file-plan.md",
		"packaging.md",
		"validation-checklist.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("cursor-sdk-backend missing %s: %v", name, err)
		}
	}
}
