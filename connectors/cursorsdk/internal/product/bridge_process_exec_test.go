package product

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/stretchr/testify/require"
)

func TestBridgeProcess_ExecFakeBridgeLifecycle(t *testing.T) {
	exe := buildFakeBridgeExe(t)
	cfg := testConfig(exe)
	cfg.ShutdownTimeout = 5 * time.Second
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: OSProcessStarter{},
		HostEnv: []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "SYSTEMDRIVE=" + os.Getenv("SYSTEMDRIVE")},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := bp.EnsureReady(ctx)
	require.NoError(t, err)
	require.Equal(t, protocol.SchemaVersion, info.SchemaVersion)
	require.EqualValues(t, 1, bp.Generation())

	health, err := bp.Call(ctx, protocol.MethodHealth, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Equal(t, protocol.TypeResponse, health.Type)

	require.NoError(t, bp.Close())
	require.NoError(t, bp.Close())
}

func buildFakeBridgeExe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fake-cursor-sdk-bridge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(dir, name)
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	pkgDir := filepath.Join(filepath.Dir(thisFile), "fakebridge")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", exe, "./cmd/fake-cursor-sdk-bridge")
	cmd.Dir = pkgDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build failed: %s", out)
	return exe
}
