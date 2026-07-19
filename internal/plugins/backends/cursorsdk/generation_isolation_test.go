package cursorsdk

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deferredWaitProc struct {
	Process
	waitStarted chan struct{}
	releaseWait chan struct{}
	signalOnce  sync.Once
}

func (p *deferredWaitProc) Wait() error {
	err := p.Process.Wait()
	p.signalOnce.Do(func() {
		if p.waitStarted != nil {
			close(p.waitStarted)
		}
	})
	if p.releaseWait != nil {
		<-p.releaseWait
	}
	return err
}

func TestBridgeProcess_StaleGenerationMustNotCloseNewerRunSubs(t *testing.T) {
	t.Parallel()
	exe := buildFakeBridgeExe(t)
	autoRespond := json.RawMessage(`{"runId":"$auto"}`)
	holdScript := fakebridge.DefaultScript()
	holdScript.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID},
	}}
	finishScript := fakebridge.DefaultScript()
	finishScript.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"ok"}`)},
		{Type: fakebridge.ActionEvent, RunID: fakebridge.AutoRunID, Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}}
	holdRaw, err := json.Marshal(holdScript)
	require.NoError(t, err)
	finishRaw, err := json.Marshal(finishScript)
	require.NoError(t, err)

	var mu sync.Mutex
	var waitStarted chan struct{}
	var releaseWait chan struct{}
	var starts atomic.Int32
	starter := processStarterFunc(func(cmd []string, cwd string, env []string) (Process, error) {
		n := starts.Add(1)
		script := holdRaw
		if n > 1 {
			script = finishRaw
		}
		env = append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+string(script))
		p, err := (OSProcessStarter{}).Start(cmd, cwd, env)
		if err != nil {
			return nil, err
		}
		if n == 1 {
			mu.Lock()
			waitStarted = make(chan struct{})
			releaseWait = make(chan struct{})
			ws, rw := waitStarted, releaseWait
			mu.Unlock()
			return &deferredWaitProc{Process: p, waitStarted: ws, releaseWait: rw}, nil
		}
		return p, nil
	})

	cfg := Config{
		APIKey:             "gen-iso-key",
		BridgeExecutable:   exe,
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 5 * time.Second,
		CancelTimeout:      time.Second,
		ShutdownTimeout:    5 * time.Second,
		MaxAgents:          4,
		MaxConcurrentRuns:  4,
		SandboxMode:        SandboxOff,
		DefaultWorkspace:   t.TempDir(),
	}
	rt := newBackendRuntime(cfg, runtimeOpts{Starter: starter, HostEnv: platformSmokeHostEnv()})
	defer func() { _ = rt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Models)
	rt.tracking.AcceptInventory(snap.Models)
	modelID := snap.Models[0].NativeID

	peerCall := liveBridgeTextCall(modelID, "peer-1", "sess-peer", "hold")
	peer, err := rt.Open(ctx, peerCall, routing.AttemptCandidate{Primary: routing.Primary{Model: modelID}})
	require.NoError(t, err)
	defer func() { _ = peer.Close() }()
	gen1 := rt.bp.Generation()
	require.EqualValues(t, 1, gen1)

	mu.Lock()
	ws, rw := waitStarted, releaseWait
	mu.Unlock()
	require.NotNil(t, ws)
	require.NotNil(t, rw)

	killErr := make(chan error, 1)
	go func() { killErr <- rt.bp.KillGeneration(ctx, gen1) }()
	select {
	case <-ws:
	case <-ctx.Done():
		t.Fatal("timeout waiting for gen1 Wait")
	}

	info, err := rt.bp.EnsureReady(ctx)
	require.NoError(t, err)
	require.Greater(t, info.Generation, gen1)

	restartCall := liveBridgeTextCall(modelID, "restart-1", "sess-restart", "after kill")
	restart, err := rt.Open(ctx, restartCall, routing.AttemptCandidate{Primary: routing.Primary{Model: modelID}})
	require.NoError(t, err)
	defer func() { _ = restart.Close() }()
	require.Greater(t, rt.bp.Generation(), gen1)

	close(rw)
	require.NoError(t, <-killErr)

	text, terminals, err := drainCanonicalStream(ctx, restart)
	require.NoError(t, err, "gen2 run must not inherit gen1 death/closeRuns")
	assert.Equal(t, 1, terminals)
	assert.NotEmpty(t, text)
	assert.False(t, errors.Is(err, ErrBridgeExited))
}

func TestFailureCoordinator_InvalidateOnBridgeDeathIsGenerationScoped(t *testing.T) {
	t.Parallel()
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	coord := NewFailureCoordinator(pool, FailureCoordinatorOpts{})

	key1 := testAgentKey("sess-gen-iso-1")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key1, Create: createParamsFor(key1), View: view(0, "", "", ""),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)
	pool.CommitSend(l1)
	require.EqualValues(t, 1, l1.Generation)
	require.Equal(t, 1, pool.LiveCount())

	// Gen2 lease exists before delayed gen1 death callback (no prior invalidate of gen1).
	bridge.setGen(2)
	key2 := testAgentKey("sess-gen-iso-2")
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key2, Create: createParamsFor(key2), View: view(0, "", "", ""),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)
	pool.CommitSend(l2)
	require.EqualValues(t, 2, l2.Generation)
	require.Equal(t, 1, pool.LiveCount(), "PrepareSend on gen2 must have disposed gen1 lease")

	coord.InvalidateOnBridgeDeath(1)
	assert.Equal(t, 1, pool.LiveCount(), "late gen1 death must not dispose gen2 leases")
	assert.EqualValues(t, 2, pool.Marker(key2).ProcessGeneration)
}

func TestBridgeProcess_RunIDReuseAcrossGenerationsUsesFreshSubscription(t *testing.T) {
	t.Parallel()
	exe := buildFakeBridgeExe(t)
	autoRespond := json.RawMessage(`{"runId":"run-shared"}`)
	holdScript := fakebridge.DefaultScript()
	holdScript.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-shared"},
	}}
	finishScript := fakebridge.DefaultScript()
	finishScript.OnAgentSend = [][]fakebridge.Action{{
		{Type: fakebridge.ActionRespond, Result: autoRespond},
		{Type: fakebridge.ActionEvent, RunID: "run-shared", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"g2"}`)},
		{Type: fakebridge.ActionEvent, RunID: "run-shared", Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}}
	holdRaw, err := json.Marshal(holdScript)
	require.NoError(t, err)
	finishRaw, err := json.Marshal(finishScript)
	require.NoError(t, err)

	var mu sync.Mutex
	var waitStarted chan struct{}
	var releaseWait chan struct{}
	var starts atomic.Int32
	starter := processStarterFunc(func(cmd []string, cwd string, env []string) (Process, error) {
		n := starts.Add(1)
		script := holdRaw
		if n > 1 {
			script = finishRaw
		}
		env = append(append([]string(nil), env...), "FAKE_BRIDGE_SCRIPT="+string(script))
		p, err := (OSProcessStarter{}).Start(cmd, cwd, env)
		if err != nil {
			return nil, err
		}
		if n == 1 {
			mu.Lock()
			waitStarted = make(chan struct{})
			releaseWait = make(chan struct{})
			ws, rw := waitStarted, releaseWait
			mu.Unlock()
			return &deferredWaitProc{Process: p, waitStarted: ws, releaseWait: rw}, nil
		}
		return p, nil
	})

	cfg := Config{
		APIKey:             "runid-reuse-key",
		BridgeExecutable:   exe,
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 5 * time.Second,
		CancelTimeout:      time.Second,
		ShutdownTimeout:    5 * time.Second,
		MaxAgents:          4,
		MaxConcurrentRuns:  4,
		SandboxMode:        SandboxOff,
		DefaultWorkspace:   t.TempDir(),
	}
	rt := newBackendRuntime(cfg, runtimeOpts{Starter: starter, HostEnv: platformSmokeHostEnv()})
	defer func() { _ = rt.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snap, err := rt.tracking.LoadModels(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, snap.Models)
	rt.tracking.AcceptInventory(snap.Models)
	modelID := snap.Models[0].NativeID

	peer, err := rt.Open(ctx, liveBridgeTextCall(modelID, "p1", "s1", "hold"),
		routing.AttemptCandidate{Primary: routing.Primary{Model: modelID}})
	require.NoError(t, err)
	gen1 := rt.bp.Generation()

	mu.Lock()
	ws, rw := waitStarted, releaseWait
	mu.Unlock()
	killErr := make(chan error, 1)
	go func() { killErr <- rt.bp.KillGeneration(ctx, gen1) }()
	select {
	case <-ws:
	case <-ctx.Done():
		t.Fatal("timeout waiting for gen1 Wait")
	}
	_, err = rt.bp.EnsureReady(ctx)
	require.NoError(t, err)

	restart, err := rt.Open(ctx, liveBridgeTextCall(modelID, "r1", "s2", "next"),
		routing.AttemptCandidate{Primary: routing.Primary{Model: modelID}})
	require.NoError(t, err)
	rs, ok := restart.(*RunStream)
	require.True(t, ok)
	require.NotNil(t, rs.frames, "reused run ID must not return nil/claimed gen1 subscription")

	close(rw)
	require.NoError(t, <-killErr)
	_ = peer.Close()

	// Drain the full stream: a prior Recv would consume the only text delta and
	// make drainCanonicalStream fail with "expected content and single terminal".
	text, terminals, err := drainCanonicalStream(ctx, restart)
	require.NoError(t, err)
	assert.Equal(t, 1, terminals)
	assert.NotEmpty(t, text)
	_ = restart.Close()
}
