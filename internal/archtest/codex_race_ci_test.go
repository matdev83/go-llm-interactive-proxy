package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const codexRaceWorkflowRel = ".github/workflows/codex-connector-race.yml"

func TestCodex_raceWorkflow_exactLinuxCommand(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(codexRaceWorkflowRel)))
	if err != nil {
		t.Fatalf("%s: %v", codexRaceWorkflowRel, err)
	}
	text := string(raw)
	if !strings.Contains(text, "ubuntu-latest") {
		t.Fatalf("%s must use ubuntu-latest", codexRaceWorkflowRel)
	}
	if !strings.Contains(text, "working-directory: connectors/codex") {
		t.Fatalf("%s must set working-directory connectors/codex", codexRaceWorkflowRel)
	}
	if !strings.Contains(text, "GOWORK=off go test -race ./...") {
		t.Fatalf("%s must run exact command GOWORK=off go test -race ./...", codexRaceWorkflowRel)
	}
}
