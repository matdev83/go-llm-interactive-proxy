package cursorsdk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSub_CloseWithBridgeExitedPreservesBufferedThenTerminal(t *testing.T) {
	t.Parallel()
	sub := newRunSub(1)
	require.NoError(t, sub.deliver(eventFrame("run-d", 1, protocol.KindTextDelta, `{"text":"hi"}`)))
	fault := BridgeExited(errors.New("exit status 3"), "stderr=noise")
	sub.closeWithErr(fault)
	sub.closeWithErr(BridgeExited(errors.New("other"), "stderr=second"))

	got := <-sub.ch
	require.Equal(t, protocol.KindTextDelta, got.Kind)
	_, ok := <-sub.ch
	require.False(t, ok)
	err := sub.TerminalErr()
	require.ErrorIs(t, err, ErrBridgeExited)
	assert.Contains(t, err.Error(), "cursor_sdk_bridge_exited")
	assert.NotContains(t, err.Error(), "second")
}

func TestBridgeProcess_ProcessExitBeforeFinishedStampsTypedBridgeExited(t *testing.T) {
	t.Parallel()
	exe := buildFakeBridgeExe(t)
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-exit"}`)},
		{Type: fakebridge.ActionSleep, Ms: 100},
		{Type: fakebridge.ActionEvent, RunID: "run-exit", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"partial"}`)},
		{Type: fakebridge.ActionStderr, Text: "secret-api-key-value path=C:\\Users\\secret-user\\ws"},
		{Type: fakebridge.ActionSleep, Ms: 100},
		{Type: fakebridge.ActionExit, Code: 3},
	}}
	raw, err := json.Marshal(script)
	require.NoError(t, err)

	cfg := Config{
		APIKey:             "secret-api-key-value",
		BridgeExecutable:   exe,
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 5 * time.Second,
		CancelTimeout:      time.Second,
		ShutdownTimeout:    5 * time.Second,
		MaxAgents:          2,
		MaxConcurrentRuns:  2,
		SandboxMode:        SandboxOff,
		DefaultWorkspace:   t.TempDir(),
	}
	starter := processStarterFunc(func(cmd []string, cwd string, env []string) (Process, error) {
		env = append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+string(raw))
		return (OSProcessStarter{}).Start(cmd, cwd, env)
	})
	rt := newBackendRuntime(cfg, runtimeOpts{Starter: starter, HostEnv: platformSmokeHostEnv()})
	defer func() { _ = rt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Models)
	rt.tracking.AcceptInventory(snap.Models)
	modelID := snap.Models[0].NativeID

	stream, err := rt.Open(ctx, liveBridgeTextCall(modelID, "exit-1", "sess-exit", "go"),
		routing.AttemptCandidate{Primary: routing.Primary{Model: modelID}})
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	var text strings.Builder
	var recvErr error
	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			recvErr = err
			break
		}
		if ev.Kind == lipapi.EventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	require.Error(t, recvErr)
	assert.Equal(t, "partial", text.String())
	assert.True(t, errors.Is(recvErr, ErrBridgeExited), "got %v", recvErr)
	var cf *ClassifiedFailure
	require.True(t, errors.As(recvErr, &cf))
	assert.Equal(t, CodeBridgeExited, cf.Code)
	msg := recvErr.Error()
	assert.NotContains(t, msg, "secret-api-key-value")
	assert.NotContains(t, msg, `C:\Users\secret-user`)
	assert.NotContains(t, strings.ToLower(msg), "bridge run ended before terminal")
}

func TestBridgeProcess_CloseDoesNotStampBridgeExitedOnIdleRuns(t *testing.T) {
	t.Parallel()
	exe := buildFakeBridgeExe(t)
	script := fakebridge.DefaultScript()
	raw, err := json.Marshal(script)
	require.NoError(t, err)
	cfg := Config{
		APIKey:             "close-key",
		BridgeExecutable:   exe,
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 5 * time.Second,
		ShutdownTimeout:    5 * time.Second,
		MaxAgents:          1,
		MaxConcurrentRuns:  1,
		SandboxMode:        SandboxOff,
		DefaultWorkspace:   t.TempDir(),
	}
	starter := processStarterFunc(func(cmd []string, cwd string, env []string) (Process, error) {
		env = append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+string(raw))
		return (OSProcessStarter{}).Start(cmd, cwd, env)
	})
	bp := newBridgeProcess(cfg, bridgeOpts{Starter: starter, HostEnv: platformSmokeHostEnv()})
	_, err = bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.NoError(t, bp.Close())
	assert.False(t, errors.Is(bp.closeErr, ErrBridgeExited))
}
