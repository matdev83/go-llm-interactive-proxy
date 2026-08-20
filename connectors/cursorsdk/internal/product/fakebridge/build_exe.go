package fakebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

var (
	buildExeOnce sync.Once
	buildExePath string
	buildExeErr  error
)

// BuildExe compiles cmd/fake-cursor-sdk-bridge into a temp directory for tests.
// Default Go tests use this deterministic binary; no Node or network required.
func BuildExe(tb testing.TB) string {
	tb.Helper()
	buildExeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fake-cursor-sdk-bridge-")
		if err != nil {
			buildExeErr = err
			return
		}
		name := "fake-cursor-sdk-bridge"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		exe := filepath.Join(dir, name)
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			buildExeErr = errors.New("runtime.Caller failed")
			return
		}
		pkgDir := filepath.Dir(thisFile)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/fake-cursor-sdk-bridge")
		cmd.Dir = pkgDir
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildExeErr = fmt.Errorf("go build fake-cursor-sdk-bridge: %w\n%s", err, out)
			return
		}
		buildExePath = exe
	})
	if buildExeErr != nil {
		tb.Fatal(buildExeErr)
	}
	return buildExePath
}
