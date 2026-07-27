package product

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
)

type raceScriptStarter struct {
	scriptJSON string
}

func (s raceScriptStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	return (OSProcessStarter{}).Start(cmd, cwd, append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+s.scriptJSON))
}

// TestParallelRace_LoserCancelClearsHistoryMarker_NoCommitRetained certifies that a
// race-loser cancel (no client-visible commit) clears the pool history marker and
// forces a fresh agent/create on the next open with the same session identity.
// This is connector-local coverage of the former root B2BUA parallel-race fixture.
func TestParallelRace_LoserCancelClearsHistoryMarker_NoCommitRetained(t *testing.T) {
	ResetLookPathCache()
	t.Cleanup(ResetLookPathCache)

	ws := t.TempDir()
	active := filepath.Join(ws, "slow-active")
	createCount := filepath.Join(ws, "creates.txt")
	exe := buildFakeBridgeExe(t)

	script := fakebridge.DefaultScript()
	script.CreateCountFile = createCount
	// First send: hold until cancel. Second send: normal finish for re-bootstrap.
	script.OnAgentSend = [][]fakebridge.Action{
		{
			{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-slow", Path: active},
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-slow"}`)},
		},
		{
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"$auto"}`)},
			{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"reboot"}`)},
			{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
		},
	}
	raw, err := json.Marshal(script)
	require.NoError(t, err)

	cfg := openTestConfig(t, exe, ws)
	cfg.CancelTimeout = 2 * time.Second
	cfg.SandboxMode = SandboxOff
	rt := newBackendRuntime(cfg, runtimeOpts{
		HostEnv: openTestHostEnv(),
		Starter: raceScriptStarter{scriptJSON: string(raw)},
	})
	t.Cleanup(func() { _ = rt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	call := textCall("gpt-5.3-codex")
	call.ID = "parallel-race-hist-1"
	call.Session.AuthoritativeSessionID = "parallel-race-hist"
	stream, err := rt.Open(ctx, call, AttemptCandidate{
		Primary: Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	loserKey := openAgentKey(t, rt, call, "gpt-5.3-codex", ws)
	require.Greater(t, rt.pool.Marker(loserKey).MessageCount, 0, "CommitSend must land before race cancel")
	waitForPathExists(t, active, 5*time.Second)

	res := stream.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelRaceLoser})
	require.Equal(t, lipapi.CancelModeProvider, res.Mode)
	_ = stream.Close()

	require.Equal(t, HistoryMarker{}, rt.pool.Marker(loserKey), "loser cancel must clear committed history marker")
	require.Equal(t, 0, rt.pool.LiveCount(), "loser entry must be invalidated")

	createsAfterRace, err := os.ReadFile(createCount)
	require.NoError(t, err)
	nAfterRace, err := strconv.Atoi(strings.TrimSpace(string(createsAfterRace)))
	require.NoError(t, err)
	require.Equal(t, 1, nAfterRace, "race open bootstraps exactly once")

	// Re-open under the same session identity the race loser used; must agent/create again.
	call2 := textCall("gpt-5.3-codex")
	call2.ID = "bootstrap-after-race"
	call2.Session.AuthoritativeSessionID = loserKey.SessionID
	stream2, err := rt.Open(ctx, call2, AttemptCandidate{
		Primary: Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	kinds := drainManaged(ctx, t, stream2)
	_ = stream2.Close()
	require.Contains(t, kinds, lipapi.EventResponseFinished)

	createsAfterBoot, err := os.ReadFile(createCount)
	require.NoError(t, err)
	nAfterBoot, err := strconv.Atoi(strings.TrimSpace(string(createsAfterBoot)))
	require.NoError(t, err)
	require.Equal(t, 2, nAfterBoot, "same session identity must bootstrap after invalidate (new agent/create)")
	require.Greater(t, rt.pool.Marker(openAgentKey(t, rt, call2, "gpt-5.3-codex", ws)).MessageCount, 0)
}

func waitForPathExists(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("path %s did not appear within %s", path, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
