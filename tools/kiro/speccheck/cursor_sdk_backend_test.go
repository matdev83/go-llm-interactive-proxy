package speccheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKiroSpec(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	filter := strings.TrimSpace(os.Getenv("KIRO_SPEC"))
	specs := registeredSpecs()
	if filter != "" {
		for _, spec := range specs {
			if spec.name == filter {
				spec.check(t, root)
				return
			}
		}
		t.Fatalf("unknown KIRO_SPEC=%q", filter)
	}
	for _, spec := range specs {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			spec.check(t, root)
		})
	}
}

type kiroSpec struct {
	name  string
	check func(t *testing.T, root string)
}

// registeredSpecs lists active spec development gates. Specs whose
// implementation is complete are archived under .kiro/specs/archive/ and
// removed here; their architecture invariants stay enforced by
// internal/archtest. The cursor-sdk-backend and windows-task-reliability
// gates were retired on closeout.
func registeredSpecs() []kiroSpec {
	return nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "lipstd")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
