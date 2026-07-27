package archtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBackendPluginReleaseGates_makefileAndCIWired(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(makefile)
	if !strings.Contains(mk, "backend-plugin-release-gates:") {
		t.Fatal("Makefile missing backend-plugin-release-gates target")
	}
	if !strings.Contains(mk, "backend-plugin-release-gates-static:") {
		t.Fatal("Makefile missing backend-plugin-release-gates-static target")
	}
	if !strings.Contains(mk, "./tools/backendplugin/release_gates") {
		t.Fatal("release gates must invoke structural release_gates tool")
	}
	if !strings.Contains(mk, ".golip-release-gates-report.json") {
		t.Fatal("release gates must emit .golip-release-gates-report.json")
	}
	if !strings.Contains(mk, "-mode=full") {
		t.Fatal("full target must use release_gates -mode=full")
	}
	for _, needle := range []string{
		"TestReleaseGates_",
		"TestParseRequirementIDs_",
		"TestValidateSelectors_",
	} {
		if !strings.Contains(mk, needle) {
			t.Fatalf("backend-plugin-release-gates-static must include %q", needle)
		}
	}
	if strings.Contains(mk, "TestDuplicate|TestUnknown|TestStrict") {
		t.Fatal("dead discovery filter fragments must be removed")
	}
	// qa must integrate the static slice without recursively calling the full target.
	qaIdx := strings.Index(mk, "\nqa:")
	if qaIdx < 0 {
		t.Fatal("Makefile missing qa target")
	}
	qaBlock := mk[qaIdx:]
	if end := strings.Index(qaBlock[1:], "\n\n"); end > 0 {
		qaBlock = qaBlock[:end+1]
	}
	if !strings.Contains(qaBlock, "backend-plugin-release-gates-static") {
		t.Fatal("make qa must integrate backend-plugin-release-gates-static")
	}
	if strings.Contains(qaBlock, "backend-plugin-release-gates\n") || strings.Contains(qaBlock, "backend-plugin-release-gates ") {
		t.Fatal("make qa must not recursively invoke full backend-plugin-release-gates")
	}

	wf := filepath.Join(root, filepath.FromSlash(".github/workflows/backend-plugin-release-gates.yml"))
	body, err := os.ReadFile(wf)
	if err != nil {
		t.Fatalf("release-gates workflow required: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "make backend-plugin-release-gates") {
		t.Fatal("workflow must run make backend-plugin-release-gates")
	}
	// Full repository/release aggregation runs on Ubuntu only. Native Windows
	// and macOS coverage remains in focused cross-platform workflows.
	var wfDoc struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					OS []string `yaml:"os"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wfDoc); err != nil {
		t.Fatalf("parse release-gates workflow: %v", err)
	}
	job, ok := wfDoc.Jobs["release-gates"]
	if !ok {
		t.Fatal("release-gates workflow missing release-gates job")
	}
	wantOS := []string{"ubuntu-latest"}
	gotOS := job.Strategy.Matrix.OS
	if !slices.Equal(gotOS, wantOS) {
		t.Fatalf("release-gates matrix os must be exactly %v; got %v", wantOS, gotOS)
	}

	crossWF := filepath.Join(root, filepath.FromSlash(".github/workflows/backend-plugin-cross-platform.yml"))
	crossBody, err := os.ReadFile(crossWF)
	if err != nil {
		t.Fatalf("cross-platform workflow required for native coverage guard: %v", err)
	}
	var crossDoc struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					OS []string `yaml:"os"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(crossBody, &crossDoc); err != nil {
		t.Fatalf("parse cross-platform workflow: %v", err)
	}
	crossJob, ok := crossDoc.Jobs["cross-platform-qa"]
	if !ok {
		t.Fatal("cross-platform workflow missing cross-platform-qa job")
	}
	wantCrossOS := []string{"ubuntu-latest", "macos-latest", "windows-latest"}
	gotCrossOS := crossJob.Strategy.Matrix.OS
	if !slices.Equal(gotCrossOS, wantCrossOS) {
		t.Fatalf("cross-platform-qa matrix os must be exactly %v; got %v", wantCrossOS, gotCrossOS)
	}

	blocker := filepath.Join(root, filepath.FromSlash(".kiro/specs/backend-connector-plugin-architecture/phase9-task95-external-release-blocker.md"))
	if _, err := os.Stat(blocker); err != nil {
		t.Fatalf("task 9.5 external blocker doc required: %v", err)
	}
}

func TestBackendPluginReleaseGates_gitignoreReportArtifact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), ".golip-release-gates-report.json") {
		t.Fatal(".gitignore must ignore release gates report")
	}
}
