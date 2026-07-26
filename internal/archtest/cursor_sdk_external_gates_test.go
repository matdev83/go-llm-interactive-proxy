package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// Cursor SDK merge posture: origin/main landed an in-tree experimental adapter
// under internal/plugins/backends/cursorsdk; the product direction remains an
// external connectors/cursorsdk executable. These gates keep both facts honest.

func TestCursorSDK_experimentalRootAdapterMayExistOutsideEssential(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "plugins", "backends", "cursorsdk")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("experimental in-tree cursorsdk adapter missing: %v", err)
	}
	for _, e := range standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		if e.ID == "cursorsdk" {
			t.Fatal("cursorsdk must not be in EssentialBackendBundle")
		}
	}
	for _, e := range standardplugins.StandardBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		if e.ID == "cursorsdk" {
			t.Fatal("cursorsdk must not be in StandardBackendBundle")
		}
	}
	reg := standardplugins.ExperimentalCursorSDKRegistration(standardplugins.UpstreamAPIKeys{})
	if strings.TrimSpace(reg.ID) != "cursorsdk" {
		t.Fatalf("ExperimentalCursorSDKRegistration id=%q want cursorsdk", reg.ID)
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

func TestCursorSDK_standardDistributionDoesNotMandateCursorSDK(t *testing.T) {
	t.Parallel()
	for _, req := range lipsdk.StandardDistributionRequirements() {
		if req.ID == "cursorsdk" {
			t.Fatal("StandardDistributionRequirements must not mandate cursorsdk")
		}
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
