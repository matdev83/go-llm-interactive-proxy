package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const acpProcessTreeWorkflowRel = ".github/workflows/acp-process-tree.yml"

func TestACP_processTreeWorkflow_nativeOSMatrix(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(acpProcessTreeWorkflowRel)))
	if err != nil {
		t.Fatalf("%s: %v", acpProcessTreeWorkflowRel, err)
	}
	text := string(raw)
	for _, runner := range []string{"ubuntu-latest", "macos-latest", "windows-latest"} {
		if !strings.Contains(text, runner) {
			t.Fatalf("%s must schedule native runner %q", acpProcessTreeWorkflowRel, runner)
		}
	}
	if !strings.Contains(text, "matrix:") || !strings.Contains(text, "os:") {
		t.Fatalf("%s must use an OS matrix", acpProcessTreeWorkflowRel)
	}
	for _, name := range []string{
		"KillProcessTree_",
		"ProcessTree_CrossCompile",
		"TestParity_",
		"TestDescribe_",
	} {
		if !strings.Contains(text, name) {
			t.Fatalf("%s must invoke -run filter containing %q", acpProcessTreeWorkflowRel, name)
		}
	}
	if !strings.Contains(text, "connector-support/acp") {
		t.Fatalf("%s must test connector-support/acp", acpProcessTreeWorkflowRel)
	}
	if !strings.Contains(text, "connectors/cursorcliacp") {
		t.Fatalf("%s must test connectors/cursorcliacp independently", acpProcessTreeWorkflowRel)
	}
	if !strings.Contains(text, `GOWORK: "off"`) && !strings.Contains(text, "GOWORK: off") {
		t.Fatalf("%s must set GOWORK=off", acpProcessTreeWorkflowRel)
	}
	if !strings.Contains(text, "shell: bash") || !strings.Contains(text, "shell: pwsh") {
		t.Fatalf("%s must use shell-appropriate bash and pwsh steps", acpProcessTreeWorkflowRel)
	}
}

func TestACP_parityCursorTarget_includesProcessTree(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	const target = "parity-cursorcliacp-plugin:"
	_, rest, ok := strings.Cut(text, target)
	if !ok {
		t.Fatalf("Makefile missing %s", target)
	}
	if next := strings.Index(rest, "\n\n"); next >= 0 {
		rest = rest[:next]
	}
	if !strings.Contains(rest, "connector-support/acp") {
		t.Fatal("parity-cursorcliacp-plugin must run connector-support/acp process-tree tests")
	}
	if !strings.Contains(rest, "KillProcessTree_") || !strings.Contains(rest, "ProcessTree_CrossCompile") {
		t.Fatal("parity-cursorcliacp-plugin must -run KillProcessTree_|ProcessTree_CrossCompile")
	}
	if !strings.Contains(rest, "connectors/cursorcliacp") {
		t.Fatal("parity-cursorcliacp-plugin must still test connectors/cursorcliacp")
	}
	if !strings.Contains(rest, "GOWORK=off") {
		t.Fatal("parity-cursorcliacp-plugin must use GOWORK=off")
	}
}
