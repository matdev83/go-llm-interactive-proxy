package qa

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/gitscope"
)

func TestCIScopeClassifier_SelfTest(t *testing.T) {
	t.Parallel()
	script := repositoryFile(t, "scripts", "ci-scope.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the remote Ubuntu workflow executes
		// the authoritative shell self-test.
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, "--self-test")
	// The shell self-test builds throwaway repositories with `git -C "$tmp"
	// init/commit`. Git exports GIT_DIR to every hook it runs, so an inherited
	// GIT_DIR would initialise and commit inside the real repository, destroying
	// its index and refs.
	cmd.Env = gitscope.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scope self-test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "OK: CI scope self-test") {
		t.Fatalf("scope self-test marker missing: %q", output)
	}
}
