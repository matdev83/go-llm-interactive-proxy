package cursorsdk

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformSmoke_FakeBridgeLane(t *testing.T) {
	if testing.Short() {
		t.Skip("platform smoke exercises subprocess lifecycle")
	}
	exe := buildFakeBridgeExe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	summary, err := RunPlatformSmoke(ctx, PlatformSmokeInput{
		BridgeExecutable: exe,
		HostEnv:          platformSmokeHostEnv(),
	})
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, runtime.GOOS, summary.GOOS)
	assert.True(t, summary.Start)
	assert.True(t, summary.Stream)
	assert.True(t, summary.Cancel)
	assert.True(t, summary.Crash)
	assert.True(t, summary.Restart)
	assert.True(t, summary.Rebootstrap)
	assert.True(t, summary.Shutdown)
	assert.Equal(t, "fake", summary.Lane)

	raw, err := json.Marshal(summary)
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, exe)
	assert.NotContains(t, text, "secret")
	assert.NotContains(t, text, "apiKey")
}

func TestProbeNativeBridgeLane_ReportsBlockedWhenNodeMissing(t *testing.T) {
	t.Parallel()
	probe := ProbeNativeBridgeLane(ProbeNativeBridgeOpts{
		LookPath: func(name string) (string, error) { return "", os.ErrNotExist },
	})
	assert.Equal(t, "blocked", probe.Status)
	assert.Contains(t, strings.ToLower(probe.Reason), "node")
}

func TestProbeNativeBridgeLane_ReadyWhenNodeAndBridgePresent(t *testing.T) {
	t.Parallel()
	probe := ProbeNativeBridgeLane(ProbeNativeBridgeOpts{
		LookPath: func(name string) (string, error) {
			if name == "node" {
				return "node-stub", nil
			}
			return "", os.ErrNotExist
		},
		NodeVersion: func(nodeExe string) (string, error) {
			assert.Equal(t, "node-stub", nodeExe)
			return "v22.13.0", nil
		},
		RunNode: func(ctx context.Context, nodeExe, bridgeBin string) (string, error) {
			assert.Equal(t, "node-stub", nodeExe)
			assert.NotEmpty(t, bridgeBin)
			return "lip-cursor-sdk-bridge 0.1.0 (@cursor/sdk 1.0.23)", nil
		},
	})
	assert.Equal(t, "ready", probe.Status)
	assert.Equal(t, "1.0.23", probe.SDKPin)
	assert.Equal(t, "v22.13.0", probe.Node)
	assert.Equal(t, runtime.GOOS, probe.GOOS)
}

func TestProbeNativeBridgeLane_BlockedWhenNodeTooOld(t *testing.T) {
	t.Parallel()
	probe := ProbeNativeBridgeLane(ProbeNativeBridgeOpts{
		LookPath:    func(string) (string, error) { return "node-stub", nil },
		NodeVersion: func(string) (string, error) { return "v20.0.0", nil },
	})
	assert.Equal(t, "blocked", probe.Status)
	assert.Contains(t, probe.Reason, "22.13")
}

func TestProbeNativeBridgeLane_CurrentHostStatusIsReadyOrBlocked(t *testing.T) {
	probe := ProbeNativeBridgeLane(ProbeNativeBridgeOpts{})
	raw, err := json.Marshal(probe)
	require.NoError(t, err)
	t.Logf("native lane probe: %s", string(raw))
	assert.Contains(t, []string{"ready", "blocked"}, probe.Status)
	assert.Equal(t, runtime.GOOS, probe.GOOS)
	assert.NotContains(t, string(raw), "apiKey")
	assert.NotContains(t, string(raw), "CURSOR_API_KEY")
	if probe.Status == "blocked" {
		assert.NotEmpty(t, probe.Reason)
	}
}

func platformSmokeHostEnv() []string {
	out := []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range []string{"SYSTEMROOT", "SYSTEMDRIVE", "HOME", "USERPROFILE", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
