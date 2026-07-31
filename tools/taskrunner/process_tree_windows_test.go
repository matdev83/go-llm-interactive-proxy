//go:build windows

package taskrunner

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestProcessTree_WindowsJobObjectDirect(t *testing.T) {
	t.Parallel()
	assertWindowsTreeCleanup(t, []string{"-mode=spawn-grandchild"})
}

func TestProcessTree_WindowsJobObjectShell(t *testing.T) {
	t.Parallel()
	helper := buildHelper(t)
	ready := t.TempDir() + `\ready`
	pid := t.TempDir() + `\pid`
	result := Run(context.Background(), Request{Argv: []string{"cmd.exe", "/C", helper, "-mode=spawn-grandchild", "-ready-file", ready, "-pid-file", pid}, Timeout: 500 * time.Millisecond, Output: Capture})
	if result.Kind != DeadlineExceeded || !result.Cleanup.Attempted || result.Cleanup.Err != nil {
		t.Fatalf("result = %#v", result)
	}
	assertFileExists(t, ready)
	assertPIDFileGone(t, pid)
}

func assertWindowsTreeCleanup(t *testing.T, args []string) {
	t.Helper()
	dir := t.TempDir()
	ready := dir + `\ready`
	pid := dir + `\pid`
	args = append(args, "-ready-file", ready, "-pid-file", pid)
	result := Run(context.Background(), Request{Argv: append([]string{buildHelper(t)}, args...), Timeout: 500 * time.Millisecond, Output: Capture})
	if result.Kind != DeadlineExceeded || !result.Cleanup.Attempted || result.Cleanup.Err != nil {
		t.Fatalf("result = %#v", result)
	}
	assertFileExists(t, ready)
	assertPIDFileGone(t, pid)
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected child marker %q: %v", path, err)
	}
}

func assertPIDFileGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.ParseUint(string(data), 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			if err == windows.ERROR_INVALID_PARAMETER {
				return
			}
			t.Fatal(err)
		}
		state, err := windows.WaitForSingleObject(h, 0)
		_ = windows.CloseHandle(h)
		if err != nil {
			t.Fatal(err)
		}
		if state == windows.WAIT_OBJECT_0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("grandchild process remained alive")
}
