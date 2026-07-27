package product

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
)

type raceScriptStarter struct {
	scriptJSON string
}

func (s raceScriptStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	return (OSProcessStarter{}).Start(cmd, cwd, append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+s.scriptJSON))
}

type raceCancelStream struct {
	inner   lipapi.ManagedEventStream
	cancels *atomic.Int32
}

func (s *raceCancelStream) Recv(ctx context.Context) (lipapi.Event, error) {
	return s.inner.Recv(ctx)
}

func (s *raceCancelStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.cancels.Add(1)
	return s.inner.Cancel(ctx, cause)
}
func (s *raceCancelStream) Close() error { return s.inner.Close() }

// raceBarrierStream delays the first Recv until waitReady closes so a parallel
// fast lane cannot win before the slow SDK arm reaches its active barrier.
type raceBarrierStream struct {
	waitReady <-chan struct{}
	waited    atomic.Bool
	inner     lipapi.ManagedEventStream
}

func (s *raceBarrierStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.waitReady != nil && !s.waited.Swap(true) {
		select {
		case <-s.waitReady:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	return s.inner.Recv(ctx)
}

func (s *raceBarrierStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}
func (s *raceBarrierStream) Close() error { return s.inner.Close() }

func TestParallelRace_LoserCancelClearsHistoryMarker_NoCommitRetained(t *testing.T) {
	ResetLookPathCache()
	t.Cleanup(ResetLookPathCache)

	ws := t.TempDir()
	active := filepath.Join(ws, "slow-active")
	createCount := filepath.Join(ws, "creates.txt")
	exe := buildFakeBridgeExe(t)

	script := fakebridge.DefaultScript()
	script.CreateCountFile = createCount
	// First send: hold until race-loser cancel. Second send: normal finish for re-bootstrap.
	script.OnAgentSend = [][]fakebridge.Action{
		{
			// Notify before Respond so Open cannot race ahead of the hold barrier.
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

	// Fast must not emit until slow Open returns (HoldUntilCancel Path + CommitSend).
	slowReady := make(chan struct{})
	var slowReadyOnce sync.Once
	releaseSlowReady := func() { slowReadyOnce.Do(func() { close(slowReady) }) }
	t.Cleanup(releaseSlowReady)

	var cancels atomic.Int32
	var loserKey AgentKey
	var sawLoserOpen atomic.Bool
	sdkBE := rt.asBackend()
	origOpen := sdkBE.Open
	sdkBE.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		stream, err := origOpen(ctx, call, cand)
		if err != nil {
			return nil, err
		}
		key := openAgentKey(t, rt, call, "gpt-5.3-codex", ws)
		loserKey = key
		sawLoserOpen.Store(true)
		require.Greater(t, rt.pool.Marker(key).MessageCount, 0, "CommitSend must land before race cancel")
		// Path is written before Respond; Open returns after Respond — barrier established.
		releaseSlowReady()
		return &raceCancelStream{inner: stream, cancels: &cancels}, nil
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{
		"slow-sdk": sdkBE,
		"fast": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return &raceBarrierStream{
					waitReady: slowReady,
					inner: lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "fast-wins"},
						{Kind: lipapi.EventResponseFinished},
					}),
				}, nil
			},
		},
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	call := &lipapi.Call{
		ID:      "parallel-race-hist-1",
		Session: lipapi.SessionRef{ContinuityKey: "parallel-race-hist"},
		Route:   lipapi.RouteIntent{Selector: "slow-sdk:gpt-5.3-codex!fast:model"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	require.NoError(t, err)
	waitForPathExists(t, active, 5*time.Second)
	col, err := lipapi.Collect(ctx, stream)
	_ = stream.Close()
	require.NoError(t, err)
	require.Equal(t, "fast-wins", col.Text.String())
	require.True(t, sawLoserOpen.Load())
	require.GreaterOrEqual(t, cancels.Load(), int32(1))

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
	stream2, err := rt.Open(ctx, call2, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
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
			t.Fatalf("timed out waiting for path %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
