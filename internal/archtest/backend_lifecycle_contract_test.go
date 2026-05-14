package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficialBackendsHaveLifecycleContractTests(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	backendsDir := filepath.Join(root, "internal", "plugins", "backends")
	entries, err := os.ReadDir(backendsDir)
	if err != nil {
		t.Fatalf("read backends: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if id == "credpool" || id == "openaicaps" || id == "openaicred" || id == "streampeek" || id == "checkcfg" {
			continue
		}
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(backendsDir, id, "lifecycle_contract_test.go")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("official backend %q must have lifecycle_contract_test.go asserting leglifecycle.BLegAttempt: %v", id, err)
			}
			if !strings.Contains(string(b), "leglifecycle.BLegAttempt") {
				t.Fatalf("official backend %q lifecycle contract test must assert leglifecycle.BLegAttempt", id)
			}
		})
	}
}
