package cursorsdk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedRunBridge struct {
	mu          sync.Mutex
	ch          chan *protocol.Frame
	cancelN     atomic.Int32
	cancelErr   error
	cancelBlock time.Duration
	unsubN      atomic.Int32
}

func newScriptedRunBridge(buf int) *scriptedRunBridge {
	if buf < 1 {
		buf = 8
	}
	return &scriptedRunBridge{ch: make(chan *protocol.Frame, buf)}
}

func (b *scriptedRunBridge) SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error) {
	_ = runID
	return b.ch, func() {
		b.unsubN.Add(1)
		b.mu.Lock()
		ch := b.ch
		b.ch = nil
		b.mu.Unlock()
		if ch != nil {
			close(ch)
		}
	}, func() error { return nil }
}

func (b *scriptedRunBridge) CancelRun(ctx context.Context, runID string) error {
	_ = runID
	b.cancelN.Add(1)
	if b.cancelBlock > 0 {
		select {
		case <-time.After(b.cancelBlock):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.cancelErr
}

func (b *scriptedRunBridge) push(f *protocol.Frame) {
	b.mu.Lock()
	ch := b.ch
	b.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- f
}

func (b *scriptedRunBridge) closeFeed() {
	b.mu.Lock()
	ch := b.ch
	b.ch = nil
	b.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

type recordingLeaseOwner struct {
	mu        sync.Mutex
	releases  int
	invalids  []InvalidationCause
	releaseEr error
}

func (o *recordingLeaseOwner) ReleaseReady(lease *AgentLease) error {
	_ = lease
	o.mu.Lock()
	defer o.mu.Unlock()
	o.releases++
	return o.releaseEr
}

func (o *recordingLeaseOwner) InvalidateLease(lease *AgentLease, cause InvalidationCause) {
	_ = lease
	o.mu.Lock()
	defer o.mu.Unlock()
	o.invalids = append(o.invalids, cause)
}

func (o *recordingLeaseOwner) snapshot() (releases int, invalids []InvalidationCause) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.releases, append([]InvalidationCause(nil), o.invalids...)
}

func drainStream(t *testing.T, s lipapi.ManagedEventStream) []lipapi.Event {
	t.Helper()
	var out []lipapi.Event
	for {
		ev, err := s.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		require.NoError(t, lipapi.ValidateEventEnvelope(&ev))
		out = append(out, ev)
	}
}

func TestRunStream_CanonicalOrderUsageTerminalEOF(t *testing.T) {
	bridge := newScriptedRunBridge(8)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-ok", AgentID: "a1", Generation: 1}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-ok", 1, protocol.KindTextDelta, `{"text":"Hello"}`))
	bridge.push(eventFrame("run-ok", 2, protocol.KindReasoningDelta, `{"text":"r"}`))
	bridge.push(eventFrame("run-ok", 3, protocol.KindUsage, `{"inputTokens":2,"outputTokens":3,"totalTokens":5}`))
	bridge.push(eventFrame("run-ok", 4, protocol.KindFinished, `{"status":"finished"}`))

	events := drainStream(t, s)
	require.NoError(t, lipapi.ValidateEventSequence(events))
	require.GreaterOrEqual(t, len(events), 5)
	assert.Equal(t, lipapi.EventResponseStarted, events[0].Kind)
	assert.Equal(t, lipapi.EventMessageStarted, events[1].Kind)
	assert.Equal(t, lipapi.EventTextDelta, events[2].Kind)
	assert.Equal(t, lipapi.EventReasoningDelta, events[3].Kind)
	assert.Equal(t, lipapi.EventUsageDelta, events[4].Kind)
	assert.Equal(t, lipapi.EventResponseFinished, events[len(events)-1].Kind)

	_, err := s.Recv(context.Background())
	assert.ErrorIs(t, err, io.EOF)

	releases, invalids := owner.snapshot()
	assert.Equal(t, 1, releases)
	assert.Empty(t, invalids)
}

func TestRunStream_ActivityIsolatedNoToolCalls(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-act"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-act", 1, protocol.KindActivity, `{"type":"tool","name":"Shell","content":"LEAK"}`))
	bridge.push(eventFrame("run-act", 2, protocol.KindTextDelta, `{"text":"ok"}`))
	bridge.push(eventFrame("run-act", 3, protocol.KindFinished, `{"status":"finished"}`))

	events := drainStream(t, s)
	for _, ev := range events {
		assert.NotEqual(t, lipapi.EventToolCallStarted, ev.Kind)
		assert.NotEqual(t, lipapi.EventToolCallArgsDelta, ev.Kind)
		assert.NotEqual(t, lipapi.EventToolCallFinished, ev.Kind)
		assert.NotContains(t, ev.WarningMessage, "LEAK")
		assert.NotContains(t, ev.Delta, "LEAK")
	}
	require.NoError(t, lipapi.ValidateEventSequence(events))
}

func TestRunStream_OutOfOrderInvalidatesLease(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-oo"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-oo", 2, protocol.KindTextDelta, `{"text":"x"}`))
	_, err := s.Recv(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), protocol.ErrorSequenceRegression)
	_, invalids := owner.snapshot()
	require.NotEmpty(t, invalids)
	assert.Equal(t, InvalidateBridge, invalids[0])
}

type blockingUnsubBridge struct {
	ch        chan *protocol.Frame
	entered   chan struct{}
	release   chan struct{}
	cancelN   atomic.Int32
	unsubN    atomic.Int32
	enterOnce sync.Once
}

func (b *blockingUnsubBridge) SubscribeRun(runID string) (<-chan *protocol.Frame, func(), func() error) {
	_ = runID
	return b.ch, func() {
		b.unsubN.Add(1)
		b.enterOnce.Do(func() { close(b.entered) })
		<-b.release
		close(b.ch)
	}, func() error { return nil }
}

func (b *blockingUnsubBridge) CancelRun(ctx context.Context, runID string) error {
	_ = runID
	b.cancelN.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func TestRunStream_TerminalUnsubCloseCancelMutualExclusion(t *testing.T) {
	bridge := &blockingUnsubBridge{
		ch:      make(chan *protocol.Frame, 4),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-race"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{CancelTimeout: time.Second})

	bridge.ch <- eventFrame("run-race", 1, protocol.KindFinished, `{"status":"finished"}`)

	done := make(chan []lipapi.Event, 1)
	errCh := make(chan error, 1)
	go func() {
		var out []lipapi.Event
		for {
			ev, err := s.Recv(context.Background())
			if errors.Is(err, io.EOF) {
				done <- out
				return
			}
			if err != nil {
				errCh <- err
				done <- out
				return
			}
			out = append(out, ev)
		}
	}()

	select {
	case <-bridge.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for unsubscribe")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s.Close()
	}()
	go func() {
		defer wg.Done()
		_ = s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	}()
	wg.Wait()
	close(bridge.release)

	var events []lipapi.Event
	select {
	case events = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout draining stream")
	}
	select {
	case err := <-errCh:
		require.NoError(t, err)
	default:
	}

	releases, invalids := owner.snapshot()
	sawFinished := false
	sawError := false
	for _, ev := range events {
		if ev.Kind == lipapi.EventResponseFinished {
			sawFinished = true
		}
		if ev.Kind == lipapi.EventError {
			sawError = true
		}
	}
	assert.LessOrEqual(t, releases, 1)
	assert.LessOrEqual(t, len(invalids), 1)
	assert.Equal(t, 1, releases+len(invalids), "exactly one lease outcome")
	assert.False(t, sawFinished && sawError, "must not emit both success and error terminals")
	if releases == 1 {
		assert.True(t, sawFinished)
		assert.False(t, sawError)
		assert.Empty(t, invalids)
	} else {
		assert.False(t, sawFinished)
		assert.True(t, sawError)
	}
}

func TestRunStream_TerminalEndsSubscriptionThenEOF(t *testing.T) {
	// Boundary: after a terminal bridge event the stream unsubscribes immediately.
	// Arbitrarily late frames after ownership ends are not stream-visible; Task 4.1
	// runSub sequencer/overflow close rejects post-terminal delivery on an active sub.
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-term"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-term", 1, protocol.KindFinished, `{"status":"finished"}`))
	events := drainStream(t, s)
	require.Equal(t, lipapi.EventResponseFinished, events[len(events)-1].Kind)
	assert.GreaterOrEqual(t, bridge.unsubN.Load(), int32(1))
	releases, invalids := owner.snapshot()
	assert.Equal(t, 1, releases)
	assert.Empty(t, invalids)
}

func TestRunStream_ReleaseReadyErrorEmitsErrorNotSuccess(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{releaseEr: ErrCommitRequired}
	lease := &AgentLease{RunID: "run-commit"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-commit", 1, protocol.KindFinished, `{"status":"finished"}`))
	events := drainStream(t, s)
	require.NotEmpty(t, events)
	assert.Equal(t, lipapi.EventError, events[len(events)-1].Kind)
	for _, ev := range events {
		assert.NotEqual(t, lipapi.EventResponseFinished, ev.Kind)
	}
	releases, invalids := owner.snapshot()
	assert.Equal(t, 1, releases)
	require.Contains(t, invalids, InvalidateUncommitted)
}

func TestRunStream_PoolMissingCommitSendFailsStream(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-no-commit")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)

	runBridge := newScriptedRunBridge(4)
	lease.RunID = "run-no-commit"
	s := NewRunStream(context.Background(), runBridge, lease, pool, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	runBridge.push(eventFrame("run-no-commit", 1, protocol.KindFinished, `{"status":"finished"}`))
	events := drainStream(t, s)
	require.Equal(t, lipapi.EventError, events[len(events)-1].Kind)
	assert.Equal(t, 0, pool.LiveCount())
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
}

func TestRunStream_PendingQueueOverflowFails(t *testing.T) {
	bridge := newScriptedRunBridge(8)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-full"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{MaxPending: 2})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-full", 1, protocol.KindTextDelta, `{"text":"x"}`))
	_, err := s.Recv(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, stream.ErrPendingQueueFull)
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateBridge)
}

func TestRunStream_DefaultPendingBoundPositive(t *testing.T) {
	s := NewRunStream(context.Background(), newScriptedRunBridge(1), &AgentLease{RunID: "r"}, &recordingLeaseOwner{}, RunStreamOpts{})
	defer func() { _ = s.Close() }()
	assert.Equal(t, defaultRunStreamPending, s.pendingCap())
	s2 := NewRunStream(context.Background(), newScriptedRunBridge(1), &AgentLease{RunID: "r2"}, &recordingLeaseOwner{}, RunStreamOpts{MaxPending: maxRunStreamPending + 100})
	defer func() { _ = s2.Close() }()
	assert.Equal(t, maxRunStreamPending, s2.pendingCap())
}

func TestRunStream_MalformedPayloadFails(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-bad"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-bad", 1, protocol.KindTextDelta, `{`))
	_, err := s.Recv(context.Background())
	require.Error(t, err)
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateBridge)
}

func TestRunStream_OversizeDeltaFails(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-big"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	huge := strings.Repeat("x", lipapi.MaxEventDeltaBytes+1)
	payload, err := json.Marshal(map[string]string{"text": huge})
	require.NoError(t, err)
	bridge.push(eventFrame("run-big", 1, protocol.KindTextDelta, string(payload)))
	_, err = s.Recv(context.Background())
	require.Error(t, err)
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateBridge)
}

func TestRunStream_CancelInvokesRunCancelAndInvalidates(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-c"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{
		CancelTimeout: time.Second,
	})
	defer func() { _ = s.Close() }()

	res := s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	assert.Equal(t, lipapi.CancelModeProvider, res.Mode)
	assert.NoError(t, res.Err)
	assert.Equal(t, int32(1), bridge.cancelN.Load())
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateCancel)

	res2 := s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	assert.Equal(t, lipapi.CancelModeProvider, res2.Mode)
	assert.Equal(t, int32(1), bridge.cancelN.Load())
}

func TestRunStream_CancelTimeoutSurfacesTransport(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	bridge.cancelBlock = 200 * time.Millisecond
	owner := &recordingLeaseOwner{}
	var escalate atomic.Int32
	lease := &AgentLease{RunID: "run-to"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{
		CancelTimeout: 20 * time.Millisecond,
		OnCancelTimeout: func(ctx context.Context) error {
			escalate.Add(1)
			return nil
		},
	})
	defer func() { _ = s.Close() }()

	res := s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelClientGone})
	assert.Equal(t, lipapi.CancelModeTransport, res.Mode)
	require.Error(t, res.Err)
	assert.Equal(t, int32(1), escalate.Load())
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateCancel)
}

func TestRunStream_CloseIdempotent(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-cl"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	assert.GreaterOrEqual(t, bridge.unsubN.Load(), int32(1))
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateCancel)
}

func TestRunStream_ContextCancelOnRecv(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-ctx"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{
		CancelTimeout: time.Second,
	})
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Recv(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, bridge.cancelN.Load(), int32(1))
}

func TestRunStream_BridgeDeathInvalidates(t *testing.T) {
	bridge := newScriptedRunBridge(2)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-dead"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.closeFeed()
	_, err := s.Recv(context.Background())
	require.Error(t, err)
	_, invalids := owner.snapshot()
	require.Contains(t, invalids, InvalidateBridge)
}

func TestRunStream_ErrorTerminalInvalidatesNoRelease(t *testing.T) {
	bridge := newScriptedRunBridge(4)
	owner := &recordingLeaseOwner{}
	lease := &AgentLease{RunID: "run-err"}
	s := NewRunStream(context.Background(), bridge, lease, owner, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	bridge.push(eventFrame("run-err", 1, protocol.KindError, `{"code":"cursor_sdk_run_failed","message":"nope"}`))
	events := drainStream(t, s)
	require.Equal(t, lipapi.EventError, events[len(events)-1].Kind)
	releases, invalids := owner.snapshot()
	assert.Equal(t, 0, releases)
	require.Contains(t, invalids, InvalidateRunError)
}

func TestRunStream_PoolHistoryCommitCallerReleaseOnSuccess(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-stream")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)
	pool.CommitSend(lease)

	runBridge := newScriptedRunBridge(4)
	lease.RunID = "run-pool"
	s := NewRunStream(context.Background(), runBridge, lease, pool, RunStreamOpts{})
	defer func() { _ = s.Close() }()

	runBridge.push(eventFrame("run-pool", 1, protocol.KindTextDelta, `{"text":"z"}`))
	runBridge.push(eventFrame("run-pool", 2, protocol.KindFinished, `{"status":"finished"}`))
	_ = drainStream(t, s)
	assert.Equal(t, 1, pool.LiveCount())
	assert.Equal(t, HistoryMarker{
		MessageCount: 1, PrefixHash: "h1", LastTurnID: "t1",
		AgentIdentityHash: key.IdentityHash(), ProcessGeneration: 1,
	}, pool.Marker(key))
}

func TestRunStream_PoolInvalidateOnCancelNoReplay(t *testing.T) {
	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { _ = pool.Close(context.Background()) }()
	key := testAgentKey("sess-stream-c")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "FULL", SuffixPrompt: "SUF",
	})
	require.NoError(t, err)
	pool.CommitSend(lease)

	runBridge := newScriptedRunBridge(2)
	lease.RunID = "run-pool-c"
	s := NewRunStream(context.Background(), runBridge, lease, pool, RunStreamOpts{CancelTimeout: time.Second})
	_ = s.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelRaceLoser})
	_ = s.Close()
	assert.Equal(t, 0, pool.LiveCount())
	assert.Equal(t, HistoryMarker{}, pool.Marker(key))
}
