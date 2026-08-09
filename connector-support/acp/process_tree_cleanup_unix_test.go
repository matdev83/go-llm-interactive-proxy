//go:build unix

package acp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	acp "github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
)

// TestKillProcessTree_UnixProcessGroup proves Setpgid + kill(-pid) removes
// parent and grandchild on unix runners (Linux CI today; macOS via
// .github/workflows/acp-process-tree.yml macos-latest — required before Task 6.3).
func TestKillProcessTree_UnixProcessGroup(t *testing.T) {
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	proc, err := acp.OSProcessStarter{}.Start([]string{
		os.Args[0], "-test.run=TestHelperProcess_UnixTreeParent$", "--",
	}, "", append(
		os.Environ(),
		"ACP_WANT_HELPER=unix-tree-parent",
		"ACP_CHILD_PID_FILE="+childPIDFile,
	))
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, childPIDFile, 8*time.Second)
	grandchild := readPIDFile(t, childPIDFile)
	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// Reap the direct child so the grandchild is reparented to init and reaped.
	// Without this, the helper stays as a zombie (kill(pid,0) succeeds for zombies)
	// and the grandchild cannot be collected by init.
	_ = proc.Wait()
	waitGone(t, proc.PID(), 8*time.Second)
	waitGone(t, grandchild, 8*time.Second)
}

func TestHelperProcess_UnixTreeParent(t *testing.T) {
	if os.Getenv("ACP_WANT_HELPER") != "unix-tree-parent" {
		return
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("ACP_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	_, _ = child.Process.Wait()
	os.Exit(0)
}

func waitFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", path)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitGone(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}
