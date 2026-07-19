package cursorsdk

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailureCoordinator_BridgeDeathInvalidatesPoolOnce(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-crash")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)
	pool.CommitSend(lease)
	require.Equal(t, 1, pool.LiveCount())

	var calls atomic.Int32
	coord := NewFailureCoordinator(pool, FailureCoordinatorOpts{
		OnInvalidate: func(cause InvalidationCause) {
			calls.Add(1)
			_ = cause
		},
	})
	coord.InvalidateOnBridgeDeath(1)
	coord.InvalidateOnBridgeDeath(1)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, 0, pool.LiveCount())
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))

	mapped := ClassifyAndMap(BridgeExited(nil, "stderr=unauthorized"), false, "api-key-one")
	assert.True(t, lipapi.IsRecoverablePreOutput(mapped))
}

func TestClassifyAndMap_PostOutputDeathNotRecoverableNoReplay(t *testing.T) {
	err := ClassifyAndMap(BridgeExited(nil, ""), true, "")
	require.Error(t, err)
	assert.False(t, lipapi.IsRecoverablePreOutput(err))
	var cf *ClassifiedFailure
	require.True(t, errors.As(err, &cf))
	assert.Equal(t, CodeBridgeExited, cf.Code)
	assert.Equal(t, lipapi.PhasePostOutput, cf.Phase)
}

func TestOnBridgeGenerationDead_InvalidationOnlyNoOrchestrationReturn(t *testing.T) {
	var gotGen atomic.Int64
	var hook OnBridgeGenerationDead = func(gen int64) {
		gotGen.Store(gen)
	}
	pool := NewSessionPool(poolTestConfig(), newFakeAgentBridge(3), SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	coord := NewFailureCoordinator(pool, FailureCoordinatorOpts{})
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		OnBridgeGenerationDead: func(gen int64) {
			hook(gen)
			coord.InvalidateOnBridgeDeath(gen)
		},
	})
	_ = bp
	hook(9)
	assert.Equal(t, int64(9), gotGen.Load())
}

func TestRunStream_MarksCommittedAndClassifiesBridgeDeath(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-out"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-out", 1, protocol.KindTextDelta, `{"text":"hi"}`))
	ev, err := s.Recv(context.Background())
	require.NoError(t, err)
	assert.Equal(t, lipapi.EventResponseStarted, ev.Kind)
	assert.True(t, s.OutputCommitted())

	for {
		ev, err = s.Recv(context.Background())
		if err != nil {
			break
		}
		if ev.Kind == lipapi.EventTextDelta {
			bridge.closeFeed()
		}
	}
	require.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF)
	assert.False(t, lipapi.IsRecoverablePreOutput(err))
	var cf *ClassifiedFailure
	require.True(t, errors.As(err, &cf))
	assert.Equal(t, CodeBridgeExited, cf.Code)
	assert.Equal(t, lipapi.PhasePostOutput, cf.Phase)
}

func TestRunStream_PreOutputBridgeDeathRecoverable(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-pre"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.closeFeed()
	_, err := s.Recv(context.Background())
	require.Error(t, err)
	assert.True(t, lipapi.IsRecoverablePreOutput(err))
	assert.False(t, s.OutputCommitted())
	assert.ErrorIs(t, err, ErrBridgeExited)
}

func TestBridgeProcess_CrashRestartNextEnsureReady(t *testing.T) {
	base := time.Now().UnixNano()
	proc1 := newFakeProc(int(base%1000000 + 960000))
	proc2 := newFakeProc(int(base%1000000 + 970000))
	var n atomic.Int32
	var deaths atomic.Int32
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		if n.Add(1) == 1 {
			go serveFakeBridgeRPC(t, proc1, nil)
			return proc1, nil
		}
		go serveFakeBridgeRPC(t, proc2, nil)
		return proc2, nil
	}}

	pool := NewSessionPool(poolTestConfig(), newFakeAgentBridge(1), SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-restart")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)
	pool.CommitSend(lease)

	coord := NewFailureCoordinator(pool, FailureCoordinatorOpts{})
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter,
		OnBridgeGenerationDead: func(gen int64) {
			deaths.Add(1)
			coord.InvalidateOnBridgeDeath(gen)
		},
	})
	defer func() { _ = bp.Close() }()

	info1, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), info1.Generation)
	require.Equal(t, 1, pool.LiveCount())

	require.NoError(t, proc1.Kill())
	require.Eventually(t, func() bool {
		return deaths.Load() > 0 && !bp.Ready()
	}, 3*time.Second, 10*time.Millisecond)

	assert.Equal(t, 0, pool.LiveCount())
	info2, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	assert.Greater(t, info2.Generation, info1.Generation)
}

func TestBridgeProcess_WaitProcExitCodeIgnoresStderrPoison(t *testing.T) {
	proc := newFakeProc(981001)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, nil)
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter,
		OnBridgeGenerationDead: func(gen int64) {
			_ = gen
		},
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	bp.mu.Lock()
	bp.stderrBuf = []byte("unauthorized invalid_api_key secret-api-key-value incompatible unsupported")
	ch := make(chan callResult, 1)
	bp.pending["poison"] = &pendingCall{ch: ch}
	bp.mu.Unlock()

	require.NoError(t, proc.Kill())
	var res callResult
	select {
	case res = <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("pending call not failed")
	}
	require.Error(t, res.err)
	got := ClassifyFailure(res.err, false, "secret-api-key-value")
	require.NotNil(t, got)
	assert.Equal(t, CodeBridgeExited, got.Code)
	assert.True(t, got.Recoverable)
	assert.ErrorIs(t, got, ErrBridgeExited)
	assert.NotContains(t, got.Error(), "secret-api-key-value")
}
