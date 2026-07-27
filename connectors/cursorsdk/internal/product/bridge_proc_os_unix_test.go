//go:build unix

package product

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetProcessGroup_unixSetpgid(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("true")
	setProcessGroup(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	require.True(t, cmd.SysProcAttr.Setpgid)
}

func TestKillProcessTree_unixProcessGroupDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Parent stays in the process group; child sleep inherits the same pgid.
	cmd := exec.CommandContext(ctx, "sh", "-c", `
set -eu
sleep 120 &
CHILD=$!
echo "$CHILD"
exec sleep 120
`)
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = killProcessTree(cmd)
			_, _ = cmd.Process.Wait()
		}
	})

	sc := bufio.NewScanner(stdout)
	require.True(t, sc.Scan(), "expected child pid on stdout")
	childPID, err := strconv.Atoi(sc.Text())
	require.NoError(t, err)
	require.Greater(t, childPID, 1)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	require.NoError(t, killProcessTree(cmd))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process-group kill did not reap parent")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant pid %d still alive after tree kill", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
