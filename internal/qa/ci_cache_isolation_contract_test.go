package qa

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The ext4 certification runs its fault checks with -race on GitHub, which builds a
// second, race-instrumented copy of the dependency tree (~1.5 GB). Sharing the
// ci-unit build cache made that the newest data, so the snapshot bound dropped the
// non-race test objects and later runs recompiled cmd/lipstd from scratch
// (Fast unit tests 60s -> 130-180s). The step must keep a throwaway GOCACHE.
func TestCIIterationSpeed_CertificationKeepsRaceObjectsOutOfSharedCache(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", "ci.yml")), &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	var found bool
	for _, step := range workflow.Jobs["test"].Steps {
		if step.Name != "Certify pinned source lifetime on ext4" {
			continue
		}
		found = true
		cache := step.Env["GOCACHE"]
		if !strings.Contains(cache, "runner.temp") || !strings.Contains(cache, "cert") {
			t.Fatalf("certification must build into its own throwaway GOCACHE under runner.temp, got %q", cache)
		}
	}
	if !found {
		t.Fatal("ci.yml test job lost the ext4 certification step")
	}
}
