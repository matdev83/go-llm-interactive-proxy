package product

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cancelKillStarter struct {
	scriptJSON string
	mu         sync.Mutex
	last       Process
	starts     atomic.Int32
	waited     atomic.Bool
}

type waitTrackProc struct {
	inner  Process
	waited *atomic.Bool
}

func (p *waitTrackProc) PID() int              { return p.inner.PID() }
func (p *waitTrackProc) Stdin() io.WriteCloser { return p.inner.Stdin() }
func (p *waitTrackProc) Stdout() io.ReadCloser { return p.inner.Stdout() }
func (p *waitTrackProc) Stderr() io.ReadCloser { return p.inner.Stderr() }
func (p *waitTrackProc) Kill() error           { return p.inner.Kill() }
func (p *waitTrackProc) Wait() error {
	err := p.inner.Wait()
	p.waited.Store(true)
	return err
}

func (s *cancelKillStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	env = append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+s.scriptJSON)
	p, err := (OSProcessStarter{}).Start(cmd, cwd, env)
	if err != nil {
		return nil, err
	}
	wrapped := &waitTrackProc{inner: p, waited: &s.waited}
	s.mu.Lock()
	s.last = wrapped
	s.mu.Unlock()
	s.starts.Add(1)
	return wrapped, nil
}

func (s *cancelKillStarter) lastPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return 0
	}
	return s.last.PID()
}

func TestOpen_CancelTimeoutKillsGenerationWithoutInjectedHook(t *testing.T) {
	ws := t.TempDir()
	gate := filepath.Join(ws, "cancel-kill-gate")
	exe := buildFakeBridgeExe(t)

	script := fakebridge.DefaultScript()
	script.OnStartup = []fakebridge.Action{{Type: fakebridge.ActionBlockCancel}}
	script.OnMethod = map[string][]fakebridge.Action{
		protocol.MethodAgentSend: {
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-cancel-hang"}`)},
			{Type: fakebridge.ActionWaitForFile, Path: gate, Ms: 60000},
			{Type: fakebridge.ActionEvent, RunID: "run-cancel-hang", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"late"}`)},
			{Type: fakebridge.ActionEvent, RunID: "run-cancel-hang", Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
		},
	}
	raw, err := json.Marshal(script)
	require.NoError(t, err)

	starter := &cancelKillStarter{scriptJSON: string(raw)}
	cfg := openTestConfig(t, exe, ws)
	cfg.CancelTimeout = 80 * time.Millisecond
	cfg.SandboxMode = SandboxOff
	rt := newBackendRuntime(cfg, runtimeOpts{
		HostEnv: openTestHostEnv(),
		Starter: starter,
	})
	t.Cleanup(func() {
		_ = os.WriteFile(gate, []byte("release"), 0o644)
		_ = rt.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	rt.tracking.AcceptInventory(snap.Models)

	call := textCall("gpt-5.3-codex")
	call.ID = "cancel-kill-1"
	stream, err := rt.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	require.NotNil(t, stream)

	rs, ok := stream.(*RunStream)
	require.True(t, ok, "Open must return *RunStream")
	require.Nil(t, rs.opts.OnCancelTimeout, "operational Open must not inject OnCancelTimeout")

	gen1 := rt.bp.Generation()
	require.Greater(t, gen1, int64(0))
	pid1 := starter.lastPID()
	require.Greater(t, pid1, 0)
	startsBefore := starter.starts.Load()

	// Peer stream subscribed on the same bridge generation must fail when kill lands.
	peer := NewRunStream(ctx, rt.agent, &AgentLease{RunID: "peer-cancel-kill", Generation: gen1}, rt.pool, RunStreamOpts{
		CancelTimeout: cfg.CancelTimeout,
	})
	defer func() { _ = peer.Close() }()
	peerErr := make(chan error, 1)
	go func() {
		_, err := peer.Recv(ctx)
		peerErr <- err
	}()

	res := stream.Cancel(ctx, lipapi.CancelCause{Kind: lipapi.CancelClientGone})
	assert.Equal(t, lipapi.CancelModeTransport, res.Mode)
	require.Error(t, res.Err)
	assert.True(t, errors.Is(res.Err, errCancelTimeout) || errors.Is(res.Err, context.DeadlineExceeded),
		"cancel must surface transport/timeout, got %v", res.Err)

	require.Eventually(t, func() bool {
		return starter.waited.Load() && !rt.bp.Ready()
	}, 5*time.Second, 20*time.Millisecond, "child process must be Wait/reaped and bridge not Ready")

	select {
	case err := <-peerErr:
		require.Error(t, err, "peer stream must fail explicitly after generation kill")
	case <-time.After(5 * time.Second):
		t.Fatal("peer stream did not fail after cancel-timeout generation kill")
	}

	require.Eventually(t, func() bool {
		return rt.pool.LiveCount() == 0
	}, 2*time.Second, 20*time.Millisecond, "OnBridgeGenerationDead must invalidate pool entries")

	// Next operation must restart a new generation successfully.
	_ = os.WriteFile(gate, []byte("release"), 0o644)
	call2 := textCall("gpt-5.3-codex")
	call2.ID = "cancel-kill-2"
	stream2, err := rt.Open(ctx, call2, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	require.NoError(t, err)
	require.NotNil(t, stream2)
	defer func() { _ = stream2.Close() }()

	gen2 := rt.bp.Generation()
	require.Greater(t, gen2, gen1, "bridge generation must advance after cancel-timeout kill")
	require.Greater(t, starter.starts.Load(), startsBefore, "next operation must spawn a new bridge process")
	assert.NotEqual(t, pid1, starter.lastPID(), "restarted process must have a new PID")

	kinds := drainManaged(ctx, t, stream2)
	require.Contains(t, kinds, lipapi.EventResponseFinished)
}

func TestBridgeProcess_KillGeneration_IdentityProtected(t *testing.T) {
	t.Parallel()
	var procs []*fakeProc
	var mu sync.Mutex
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		p := newFakeProc(940000 + len(procs))
		mu.Lock()
		procs = append(procs, p)
		mu.Unlock()
		go serveFakeBridgeRPC(t, p, nil)
		return p, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	gen1 := bp.Generation()

	bp.testKillCurrentWithoutClose()
	require.Eventually(t, func() bool { return !bp.Ready() }, time.Second, 5*time.Millisecond)
	_, err = bp.EnsureReady(context.Background())
	require.NoError(t, err)
	gen2 := bp.Generation()
	require.Greater(t, gen2, gen1)

	mu.Lock()
	p2 := procs[len(procs)-1]
	mu.Unlock()
	killsBefore := p2.killCount.Load()

	require.NoError(t, bp.KillGeneration(context.Background(), gen1),
		"KillGeneration for stale gen must no-op without error")
	assert.Equal(t, killsBefore, p2.killCount.Load(), "stale KillGeneration must not kill current process")

	require.NoError(t, bp.KillGeneration(context.Background(), gen2))
	require.Eventually(t, func() bool {
		return p2.killCount.Load() > killsBefore || !bp.Ready()
	}, time.Second, 5*time.Millisecond, "KillGeneration(current) must kill/reap the owned process")
}
