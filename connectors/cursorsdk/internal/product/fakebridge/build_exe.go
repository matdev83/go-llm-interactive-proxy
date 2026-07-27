package fakebridge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// BuildExe compiles cmd/fake-cursor-sdk-bridge into a temp directory for tests.
// Default Go tests use this deterministic binary; no Node or network required.
func BuildExe(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	name := "fake-cursor-sdk-bridge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(dir, name)
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(thisFile)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/fake-cursor-sdk-bridge")
	cmd.Dir = pkgDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		tb.Fatalf("go build fake-cursor-sdk-bridge: %v\n%s", err, out)
	}
	return exe
}
