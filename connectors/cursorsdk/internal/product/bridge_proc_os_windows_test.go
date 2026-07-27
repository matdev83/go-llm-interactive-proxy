//go:build windows

package product

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetProcessGroup_windowsCreateNewProcessGroup(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/C", "exit", "0")
	setProcessGroup(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	require.NotZero(t, cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP)
}

func TestKillProcessTree_windowsTaskkill(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/C", "waitfor", "/T", "30", "LipBridgeKillTestSignal")
	setProcessGroup(cmd)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	require.NoError(t, killProcessTree(cmd))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process tree kill did not reap child")
	}
}
