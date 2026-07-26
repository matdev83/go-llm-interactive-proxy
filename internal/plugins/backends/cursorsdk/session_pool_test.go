package cursorsdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAgentBridge struct {
	mu         sync.Mutex
	gen        int64
	creates    atomic.Int32
	sends      atomic.Int32
	disposes   atomic.Int32
	lastPrompt string
	prompts    []string
	createErr  error
	sendErr    error
	agents     map[string]bool
	agentSeq   atomic.Int32
	runSeq     atomic.Int32
}

func newFakeAgentBridge(gen int64) *fakeAgentBridge {
	return &fakeAgentBridge{gen: gen, agents: map[string]bool{}}
}

func (f *fakeAgentBridge) Generation() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func (f *fakeAgentBridge) EnsureReady(ctx context.Context) (BridgeInfo, error) {
	if err := ctx.Err(); err != nil {
		return BridgeInfo{}, err
	}
	f.mu.Lock()
	gen := f.gen
	f.mu.Unlock()
	return BridgeInfo{Generation: gen, SchemaVersion: protocol.SchemaVersion}, nil
}

func (f *fakeAgentBridge) CreateAgent(ctx context.Context, params protocol.AgentCreateParams) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.creates.Add(1)
	if f.createErr != nil {
		return "", f.createErr
	}
	if params.EnableAgentRetries {
		panic("enableAgentRetries must be false")
	}
	id := fmt.Sprintf("agent-%d", f.agentSeq.Add(1))
	f.mu.Lock()
	f.agents[id] = true
	f.mu.Unlock()
	return id, nil
}

func (f *fakeAgentBridge) SendAgent(ctx context.Context, agentID, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.sends.Add(1)
	f.mu.Lock()
	f.lastPrompt = prompt
	f.prompts = append(f.prompts, prompt)
	alive := f.agents[agentID]
	f.mu.Unlock()
	if !alive {
		return "", errors.New("unknown agent")
	}
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return fmt.Sprintf("run-%d", f.runSeq.Add(1)), nil
}

func (f *fakeAgentBridge) DisposeAgent(ctx context.Context, agentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.disposes.Add(1)
	f.mu.Lock()
	delete(f.agents, agentID)
	f.mu.Unlock()
	return nil
}

func (f *fakeAgentBridge) SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error) {
	ch := make(chan *protocol.Frame)
	return ch, func() { close(ch) }, func() error { return nil }
}

func (f *fakeAgentBridge) CancelRun(ctx context.Context, runID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = runID
	return nil
}

func (f *fakeAgentBridge) setGen(gen int64) {
	f.mu.Lock()
	f.gen = gen
	f.mu.Unlock()
}

func (f *fakeAgentBridge) aliveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.agents)
}

func poolTestConfig() Config {
	cfg := testConfig("/bridge/exe")
	cfg.MaxAgents = 4
	cfg.MaxConcurrentRuns = 2
	cfg.AgentIdleTimeout = time.Minute
	return cfg
}

func testAgentKey(session string) AgentKey {
	return AgentKey{
		SessionID:           session,
		Workspace:           "/ws",
		ModelID:             "model-a",
		KeyFingerprint:      FingerprintSecret("api-key-one"),
		SettingsFingerprint: FingerprintSettingSources([]SettingSource{SettingSourceProject}),
		MCPFingerprint:      FingerprintJSON([]byte(`{}`)),
		Sandbox:             SandboxRequired,
	}
}

func createParamsFor(key AgentKey) protocol.AgentCreateParams {
	return protocol.AgentCreateParams{
		APIKey: "api-key-one",
		Model:  protocol.ModelSelection{ID: key.ModelID},
		Local:  protocol.AgentCreateLocal{Cwd: key.Workspace},
	}
}

func view(n int, allHash, headHash, turn string) TranscriptView {
	return TranscriptView{MessageCount: n, PrefixHash: allHash, HeadPrefixHash: headHash, LastTurnID: turn}
}

func finishTurn(t *testing.T, pool *SessionPool, lease *AgentLease) {
	t.Helper()
	pool.CommitSend(lease)
	require.NoError(t, pool.ReleaseReady(lease))
}

func TestSessionPool_BootstrapThenIncrementalCommit(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-a")

	lease1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key),
		View: view(1, "h1", "", "t1"), FullPrompt: "FULL:1", SuffixPrompt: "SUF:1",
	})
	require.NoError(t, err)
	require.Equal(t, HistoryBootstrap, lease1.Mode)
	assert.Equal(t, "FULL:1", bridge.lastPrompt)
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
	finishTurn(t, pool, lease1)
	assert.Equal(t, 1, pool.Marker(key).MessageCount)

	lease2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key),
		View: view(2, "h2", "h1", "t2"), FullPrompt: "FULL:2", SuffixPrompt: "SUF:2",
	})
	require.NoError(t, err)
	require.Equal(t, HistoryIncremental, lease2.Mode)
	assert.Equal(t, "SUF:2", bridge.lastPrompt)
	assert.Equal(t, lease1.AgentID, lease2.AgentID)
	assert.EqualValues(t, 1, bridge.creates.Load())
	finishTurn(t, pool, lease2)
}

func TestSessionPool_IdentityComponentChangeDisposesOld(t *testing.T) {
	base := testAgentKey("sess-id")
	type mut struct {
		name string
		fn   func(*AgentKey)
	}
	mutations := []mut{
		{"model", func(k *AgentKey) { k.ModelID = "model-b" }},
		{"workspace", func(k *AgentKey) { k.Workspace = "/other" }},
		{"key", func(k *AgentKey) { k.KeyFingerprint = FingerprintSecret("api-key-two") }},
		{"settings", func(k *AgentKey) {
			k.SettingsFingerprint = FingerprintSettingSources([]SettingSource{SettingSourceUser})
		}},
		{"mcp", func(k *AgentKey) { k.MCPFingerprint = FingerprintJSON([]byte(`{"x":1}`)) }},
		{"sandbox", func(k *AgentKey) { k.Sandbox = SandboxOff }},
		{"autoReview", func(k *AgentKey) { k.AutoReview = true }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			bridge := newFakeAgentBridge(1)
			pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
			defer func() { _ = pool.Close(context.Background()) }()
			key := base
			l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
				Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
			})
			require.NoError(t, err)
			finishTurn(t, pool, l1)
			oldID := l1.AgentID
			key2 := key
			tc.fn(&key2)
			l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
				Key: key2, Create: createParamsFor(key2), View: view(1, "h1", "", "t1"), FullPrompt: "F2", SuffixPrompt: "S2",
			})
			require.NoError(t, err)
			require.Equal(t, HistoryBootstrap, l2.Mode)
			assert.NotEqual(t, oldID, l2.AgentID)
			assert.Equal(t, 1, pool.LiveCount())
			assert.GreaterOrEqual(t, bridge.disposes.Load(), int32(1))
			assert.Equal(t, HistoryMarker{}, pool.Marker(key))
			finishTurn(t, pool, l2)
		})
	}
}

func TestSessionPool_DifferentSessionIDsConcurrent(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	k1, k2 := testAgentKey("s1"), testAgentKey("s2")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k1, Create: createParamsFor(k1), View: view(1, "a", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k2, Create: createParamsFor(k2), View: view(1, "b", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, pool.LiveCount())
	finishTurn(t, pool, l1)
	finishTurn(t, pool, l2)
}

func TestSessionPool_FailedCreateRollbackAndRetry(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	secret := "api-key-one"
	bridge.createErr = fmt.Errorf("upstream rejected key %s", secret)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-cfail")
	_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.Contains(t, err.Error(), "[REDACTED]")
	assert.Equal(t, 0, pool.LiveCount())
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
	assert.Equal(t, 0, bridge.aliveCount())

	bridge.createErr = nil
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l2)
	assert.Equal(t, 1, pool.LiveCount())
}

func TestSessionPool_EmptyPromptRejected(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-empty")
	_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "  ", SuffixPrompt: "S",
	})
	require.ErrorIs(t, err, ErrEmptyPrompt)
	assert.EqualValues(t, 0, bridge.creates.Load())

	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l1)
	_, err = pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(2, "h2", "h1", "t2"), FullPrompt: "F2", SuffixPrompt: "",
	})
	require.ErrorIs(t, err, ErrEmptyPrompt)
	assert.EqualValues(t, 1, bridge.sends.Load())
}

func TestSessionPool_ReleaseReadyBeforeCommitInvalidates(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-commit")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	err = pool.ReleaseReady(l1)
	require.ErrorIs(t, err, ErrCommitRequired)
	assert.Equal(t, 0, pool.LiveCount())
	assert.GreaterOrEqual(t, bridge.disposes.Load(), int32(1))
}

func TestSessionPool_UncommittedNotReusable(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-pending")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(2, "h2", "h1", "t2"), FullPrompt: "F2", SuffixPrompt: "S2",
	})
	require.NoError(t, err)
	assert.NotEqual(t, l1.AgentID, l2.AgentID)
	assert.Equal(t, 1, pool.LiveCount())
	assert.Equal(t, HistoryBootstrap, l2.Mode)
	pool.CommitSend(l1)
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
	finishTurn(t, pool, l2)
}

func TestSessionPool_FailedSendDoesNotCommit(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	bridge.sendErr = errors.New("send boom")
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-fail")
	_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.Error(t, err)
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
	assert.Equal(t, 0, pool.LiveCount())
}

func TestSessionPool_SendBridgeExitedClassifiedRecoverablePreOutput(t *testing.T) {
	secret := "api-key-one"
	bridge := newFakeAgentBridge(1)
	bridge.sendErr = BridgeExited(nil, "stderr=unauthorized "+secret)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-send-exit")

	_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.Error(t, err)
	assert.True(t, lipapi.IsRecoverablePreOutput(err), "send-time BridgeExited must be RecoverablePreOutput, got %v", err)
	assert.ErrorIs(t, err, ErrBridgeExited)

	var cf *ClassifiedFailure
	require.True(t, errors.As(err, &cf), "want ClassifiedFailure in chain, got %T %v", err, err)
	require.NotNil(t, cf)
	assert.Equal(t, CodeBridgeExited, cf.Code)
	assert.Equal(t, lipapi.PhasePreOutput, cf.Phase)
	assert.True(t, cf.Recoverable)
	assert.NotContains(t, err.Error(), secret)
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
	assert.Equal(t, 0, pool.LiveCount())
}

func TestSessionPool_CreateBridgeExitedNotClassifiedRecoverable(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	bridge.createErr = BridgeExited(nil, "create-exit")
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-create-exit")

	_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.Error(t, err)
	assert.False(t, lipapi.IsRecoverablePreOutput(err), "create-time errors must stay unmapped, got %v", err)
	assert.ErrorIs(t, err, ErrBridgeExited)
	assert.Equal(t, 0, pool.LiveCount())
}

func TestSessionPool_SameKeyBusyConflict(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-busy")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	pool.CommitSend(l1)
	_, err = pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(2, "h2", "h1", "t2"), FullPrompt: "F2", SuffixPrompt: "S2",
	})
	require.ErrorIs(t, err, ErrAgentBusy)
	require.NoError(t, pool.ReleaseReady(l1))
}

func TestSessionPool_RunLimitDistinctFromAgentLimit(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	cfg := poolTestConfig()
	cfg.MaxConcurrentRuns = 2
	cfg.MaxAgents = 4
	pool := NewSessionPool(cfg, bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	k1, k2, k3 := testAgentKey("c1"), testAgentKey("c2"), testAgentKey("c3")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k1, Create: createParamsFor(k1), View: view(1, "a1", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k2, Create: createParamsFor(k2), View: view(1, "b1", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	_, err = pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k3, Create: createParamsFor(k3), View: view(1, "c1", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.ErrorIs(t, err, ErrRunLimit)
	finishTurn(t, pool, l1)
	finishTurn(t, pool, l2)

	cfg2 := poolTestConfig()
	cfg2.MaxAgents = 1
	cfg2.MaxConcurrentRuns = 2
	bridge2 := newFakeAgentBridge(1)
	pool2 := NewSessionPool(cfg2, bridge2, SessionPoolOpts{})
	defer func() { _ = pool2.Close(context.Background()) }()
	a1, err := pool2.PrepareSend(context.Background(), PrepareSendInput{
		Key: testAgentKey("x1"), Create: createParamsFor(testAgentKey("x1")), View: view(1, "a", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	_, err = pool2.PrepareSend(context.Background(), PrepareSendInput{
		Key: testAgentKey("x2"), Create: createParamsFor(testAgentKey("x2")), View: view(1, "b", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.ErrorIs(t, err, ErrAgentLimit)
	finishTurn(t, pool2, a1)
}

func TestSessionPool_MaxAgentExhaustionEvictsIdleOnly(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	cfg := poolTestConfig()
	cfg.MaxAgents = 2
	cfg.MaxConcurrentRuns = 2
	pool := NewSessionPool(cfg, bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	k1, k2, k3 := testAgentKey("e1"), testAgentKey("e2"), testAgentKey("e3")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k1, Create: createParamsFor(k1), View: view(1, "a", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l1)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k2, Create: createParamsFor(k2), View: view(1, "b", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	l3, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: k3, Create: createParamsFor(k3), View: view(1, "c", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, pool.LiveCount())
	assert.Equal(t, HistoryMarker{}, pool.Marker(k1))
	finishTurn(t, pool, l2)
	finishTurn(t, pool, l3)
}

func TestSessionPool_PrepareSendReapsIdle(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	cfg := poolTestConfig()
	cfg.AgentIdleTimeout = 10 * time.Second
	now := time.Unix(1_700_000_000, 0)
	pool := NewSessionPool(cfg, bridge, SessionPoolOpts{Now: func() time.Time { return now }})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("idle")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l1)
	now = now.Add(11 * time.Second)
	other := testAgentKey("other")
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: other, Create: createParamsFor(other), View: view(1, "o", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
	assert.Equal(t, 1, pool.LiveCount())
	finishTurn(t, pool, l2)
}

func TestSessionPool_BridgeGenerationInvalidates(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("gen")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l1)
	old := l1.AgentID
	bridge.setGen(2)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(2, "h2", "h1", "t2"), FullPrompt: "F2", SuffixPrompt: "S2",
	})
	require.NoError(t, err)
	assert.NotEqual(t, old, l2.AgentID)
	require.Equal(t, HistoryBootstrap, l2.Mode)
	finishTurn(t, pool, l2)
}

func TestSessionPool_InvalidateOnCancelOrRunError(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("out")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	pool.CommitSend(l1)
	pool.InvalidateLease(l1, InvalidateCancel)
	assert.Equal(t, 0, pool.LiveCount())
}

func TestSessionPool_CloseIdempotentAndConcurrentPrepare(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	key := testAgentKey("close")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	pool.CommitSend(l1)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := testAgentKey(fmt.Sprintf("c-%d", i))
			_, err := pool.PrepareSend(context.Background(), PrepareSendInput{
				Key: k, Create: createParamsFor(k), View: view(1, "h", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Go(func() {
		errs <- pool.Close(context.Background())
	})
	wg.Wait()
	close(errs)
	require.NoError(t, pool.Close(context.Background()))
	_, err = pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: testAgentKey("after"), Create: createParamsFor(testAgentKey("after")), View: view(1, "h", "", "t"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.ErrorIs(t, err, ErrPoolClosed)
	assert.Equal(t, 0, pool.LiveCount())
	for err := range errs {
		if err == nil || errors.Is(err, ErrPoolClosed) || errors.Is(err, ErrRunLimit) || errors.Is(err, ErrAgentLimit) || errors.Is(err, ErrAgentBusy) {
			continue
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionPool_NoAgentResumePath(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("resume")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l1)
	pool.InvalidateAll(InvalidateBridge)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	require.Equal(t, HistoryBootstrap, l2.Mode)
	assert.EqualValues(t, 2, bridge.creates.Load())
	finishTurn(t, pool, l2)
}

func TestSessionPool_SubscribeRunHookForLaterStream(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sub")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"), FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	ch, cancel, _ := bridge.SubscribeRun(l1.RunID)
	defer cancel()
	require.NotNil(t, ch)
	finishTurn(t, pool, l1)
}

func TestRedactSecret_stripsKey(t *testing.T) {
	t.Parallel()
	err := redactSecret(errors.New("boom api-key-one end"), "api-key-one")
	require.NotContains(t, err.Error(), "api-key-one")
	require.True(t, strings.Contains(err.Error(), "[REDACTED]"))
}
