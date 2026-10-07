package qa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
)

var (
	lipstdBinOnce sync.Once
	lipstdBinPath string
	lipstdBinErr  error
)

func getLipstdBinary(tb testing.TB, root string) string {
	tb.Helper()
	lipstdBinOnce.Do(func() {
		if prebuilt := os.Getenv("LIP_QA_LIPSTD_BINARY"); prebuilt != "" {
			if _, err := os.Stat(prebuilt); err != nil {
				lipstdBinErr = fmt.Errorf("stat prebuilt lipstd: %w", err)
				return
			}
			lipstdBinPath = prebuilt
			return
		}
		dir, err := os.MkdirTemp("", "lipstd-qa-bin-")
		if err != nil {
			lipstdBinErr = err
			return
		}
		name := "lipstd"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		dst := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", dst, "./cmd/lipstd")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			lipstdBinErr = fmt.Errorf("build lipstd: %w\n%s", err, out)
			return
		}
		lipstdBinPath = dst
	})
	if lipstdBinErr != nil {
		tb.Fatal(lipstdBinErr)
	}
	return lipstdBinPath
}

// TestDualPlaneMigrationCheckConfigDogfoodStub runs the distributed lipstd
// binary against the dogfood-local-stub backendplugin configuration. The
// operator-facing rollout narrative in docs/dual-plane-migration-rollout.md is
// human-reviewed; the machine-checkable guarantee is that a real lipstd build
// accepts the distributed connector configuration.
func TestDualPlaneMigrationCheckConfigDogfoodStub(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	cfg := bpkit.WriteDogfoodLocalStubConfig(t)
	bin := getLipstdBinary(t, root)
	cmd := exec.Command(bin, "check-config", "--config", cfg)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check-config dogfood-local-stub: %v\n%s", err, out)
	}
}
