package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase85_MakefileTargetsExist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mk := readFile(t, filepath.Join(root, "Makefile"))
	for _, want := range []string{
		"isolated-root-qa:",
		"installed-plugin-smoke:",
	} {
		if !strings.Contains(mk, want) {
			t.Fatalf("Makefile missing target %q (Phase 8.5)", want)
		}
	}
	if !strings.Contains(mk, "GOWORK=off") && !strings.Contains(mk, "isolated-root-qa") {
		t.Fatal("Makefile Phase 8.5 wiring incomplete")
	}
	phony := firstPHONYLine(mk)
	for _, tgt := range []string{"isolated-root-qa", "installed-plugin-smoke"} {
		if !strings.Contains(phony, tgt) {
			t.Fatalf(".PHONY must include %s", tgt)
		}
	}
}

func TestPhase85_ScriptsExistAndUseGOWORKOff(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"scripts/isolated-root-qa.ps1",
		"scripts/isolated-root-qa.sh",
		"scripts/installed-plugin-smoke.ps1",
		"scripts/installed-plugin-smoke.sh",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Phase 8.5 missing %s: %v", rel, err)
		}
		text := string(raw)
		if !strings.Contains(text, "GOWORK") || !strings.Contains(strings.ToLower(text), "off") {
			t.Fatalf("%s must set GOWORK=off", rel)
		}
	}
}

func TestPhase85_IsolatedRootExclusionsAreStructural(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"scripts/isolated-root-qa.ps1",
		"scripts/isolated-root-qa.sh",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		if !strings.Contains(string(raw), "isolated_root_qa") {
			t.Fatalf("%s must invoke tools/backendplugin/isolated_root_qa", rel)
		}
	}
	tool := filepath.Join(root, "tools", "backendplugin", "isolated_root_qa", "main.go")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatalf("missing isolated_root_qa tool: %v", err)
	}
	text := string(raw)
	for _, dir := range []string{"connectors", "connector-support", "node_modules"} {
		if !strings.Contains(text, dir) {
			t.Fatalf("isolated_root_qa must exclude %q structurally", dir)
		}
	}
	for _, bad := range []string{`"openrouter"`, `"opencode"`, `"openai-codex"`} {
		if strings.Contains(text, bad) {
			t.Fatalf("isolated_root_qa must not hardcode connector product name %s", bad)
		}
	}
}

func TestPhase85_InstalledSmokeUsesReleaseMetadataNotNameList(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"scripts/installed-plugin-smoke.ps1",
		"scripts/installed-plugin-smoke.sh",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		text := string(raw)
		if !strings.Contains(text, "installed_plugin_smoke") {
			t.Fatalf("%s must invoke tools/backendplugin/installed_plugin_smoke", rel)
		}
	}
	tool := filepath.Join(root, "tools", "backendplugin", "installed_plugin_smoke", "main.go")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatalf("missing installed_plugin_smoke tool: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "release.yaml") || !strings.Contains(text, "discoverReleases") {
		t.Fatal("installed_plugin_smoke must discover artifacts via release.yaml metadata")
	}
	for _, bad := range []string{
		`selectCSV := "localstub,opencode"`,
		`-select localstub,opencode`,
		`[]string{"localstub", "opencode"}`,
		`[]string{"localstub", "codex"}`,
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("installed_plugin_smoke hardcodes connector-name selection list: %s", bad)
		}
	}
	if !strings.Contains(text, "sha256") && !strings.Contains(text, "SHA256") {
		t.Fatal("installed_plugin_smoke must prove binary hash unchanged across plugin install")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func firstPHONYLine(mk string) string {
	for line := range strings.SplitSeq(mk, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ".PHONY:") {
			return line
		}
	}
	return ""
}
