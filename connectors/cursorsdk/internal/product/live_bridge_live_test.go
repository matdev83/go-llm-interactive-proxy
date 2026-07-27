//go:build cursorsdk_live_bridge

package product

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLiveBridgeHarness_Live runs only under -tags=cursorsdk_live_bridge with
// CURSOR_SDK_LIVE=1 and a nonempty CURSOR_API_KEY. Ordinary go test never selects this file.
func TestLiveBridgeHarness_Live(t *testing.T) {
	if ready, reason := LiveOptInReady(os.Getenv); !ready {
		t.Skip(reason)
	}
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	pkgRoot := filepath.Dir(thisFile)
	connectorRoot := filepath.Clean(filepath.Join(pkgRoot, "..", ".."))
	bridgeRoot := filepath.Join(connectorRoot, "bridge-node")
	bin := filepath.Join(bridgeRoot, "bin", "lip-cursor-sdk-bridge.js")
	if _, err := os.Stat(bin); err != nil {
		if _, errMod := os.Stat(filepath.Join(bridgeRoot, "node_modules")); errMod != nil {
			t.Fatalf("bridge bin missing and node_modules absent; build bridge offline first")
		}
		cmd := exec.Command("npm", "run", "build")
		cmd.Dir = bridgeRoot
		cmd.Env = liveBridgeHostEnv(os.Environ())
		require.NoError(t, cmd.Run(), "bridge package build failed")
	}

	argv, err := buildLiveBridgeCommand(liveBridgeCommandOpts{BridgeRoot: connectorRoot, BridgeBin: bin})
	require.NoError(t, err)

	ws, err := os.MkdirTemp("", "lip-live-bridge-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(ws) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	summary, err := runLiveBridgeHarness(ctx, liveBridgeInput{
		Getenv:     os.Getenv,
		BridgeArgv: argv,
		Workspace:  ws,
		HostEnv:    os.Environ(),
		Timeout:    4 * time.Minute,
	})
	require.NotNil(t, summary)
	raw, marshalErr := json.Marshal(summary)
	require.NoError(t, marshalErr)
	fmt.Println(string(raw))
	require.NoError(t, err)
	require.Contains(t, []string{"complete", "blocked", "failed"}, summary.Status)
}
