package qa

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const ownerCallbackGateTest = "TestRuntimebundle_NoCompleteOwnerCallbackEscapes"

// The owner-callback escape gate type-checks the whole module for two operating
// systems (~2 min alone) and used to set the wall time of the Go suite job. It
// runs in its own parallel job; the suite must skip it and the required
// repo-hygiene aggregate must wait for that job, or the gate would run nowhere.
func TestCIIterationSpeed_OwnerCallbackGateRunsInParallelJob(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", "ci.yml")), &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}

	stepRun := func(job, name string) string {
		for _, step := range workflow.Jobs[job].Steps {
			if step.Name == name {
				return step.Run
			}
		}
		t.Fatalf("ci.yml job %q lost step %q", job, name)
		return ""
	}

	suite := stepRun("suite", "Complete root suite with precommit tags")
	if !strings.Contains(suite, "skip='"+ownerCallbackGateTest+"'") || !strings.Contains(suite, `-skip "^(${skip})\$"`) {
		t.Errorf("suite must pass %s to go test -skip (it runs in owner-gate)", ownerCallbackGateTest)
	}
	if !strings.Contains(suite, "BASH_REMATCH") {
		t.Error("suite must merge its -skip with the quarantine -skip in GOFLAGS; go keeps only the last one")
	}

	gate := stepRun("owner-gate", "Owner callback escape gate")
	if !strings.Contains(gate, "-run '^"+ownerCallbackGateTest+"$'") {
		t.Errorf("owner-gate must run exactly %s, got %q", ownerCallbackGateTest, gate)
	}

	needs, _ := workflow.Jobs["repo-hygiene"].Needs.([]any)
	var waits bool
	for _, n := range needs {
		if n == "owner-gate" {
			waits = true
		}
	}
	if !waits {
		t.Error("repo-hygiene must need owner-gate")
	}
	if !strings.Contains(stepRun("repo-hygiene", "Require scope, preflight, database parity, billing certification, Go suite, owner-callback gate and lint success"), "OWNER_GATE_RESULT") {
		t.Error("repo-hygiene must fail when owner-gate fails")
	}
}
