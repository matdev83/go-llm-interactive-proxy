package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendPluginCrossPlatform_makefileAndCIWired(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	if !strings.Contains(mk, "backend-plugin-cross-platform-qa:") {
		t.Fatal("Makefile missing backend-plugin-cross-platform-qa target")
	}
	if !strings.Contains(mk, "./tools/backendplugin/crossplatform_qa") {
		t.Fatal("cross-platform QA must invoke structural crossplatform_qa tool")
	}
	if !strings.Contains(mk, ".golip-crossplatform-matrix.json") {
		t.Fatal("cross-platform QA must emit .golip-crossplatform-matrix.json")
	}
	for _, needle := range []string{
		"TestAdversarial_",
		"TestActivate_",
		"TestStream_",
		"KillProcessTree_",
		"package-plugin-smoke",
	} {
		if !strings.Contains(mk, needle) {
			t.Fatalf("backend-plugin-cross-platform-qa must include native/package gate %q", needle)
		}
	}

	wf := filepath.Join(root, filepath.FromSlash(".github/workflows/backend-plugin-cross-platform.yml"))
	body, err := os.ReadFile(wf)
	if err != nil {
		t.Fatalf("cross-platform workflow required: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "make backend-plugin-cross-platform-qa") {
		t.Fatal("workflow must run make backend-plugin-cross-platform-qa")
	}
	for _, osName := range []string{"ubuntu-latest", "macos-latest", "windows-latest"} {
		if !strings.Contains(text, osName) {
			t.Fatalf("workflow must include %s runner", osName)
		}
	}

	evidence := filepath.Join(root, filepath.FromSlash(".kiro/specs/archive/backend-connector-plugin-architecture/phase9-task94-external-cross-platform-blocker.md"))
	if _, err := os.Stat(evidence); err != nil {
		t.Fatalf("task 9.4 archived cross-platform evidence required: %v", err)
	}
}

func TestBackendPluginCrossPlatform_hostProfilesRejectFalseDarwinClaims(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "connectors"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		tmpl := filepath.Join(root, "connectors", e.Name(), "manifest", "template.backendplugin.json")
		b, err := os.ReadFile(tmpl)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), `"os": "darwin"`) || strings.Contains(string(b), `"os":"darwin"`) {
			t.Fatalf("%s claims darwin despite host channel fail-closed", e.Name())
		}
	}
}
