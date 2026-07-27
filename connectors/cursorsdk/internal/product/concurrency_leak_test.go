package product

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestConcurrency_PrepareSendAllSucceedNoLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const workers = 4
	bridge := newFakeAgentBridge(1)
	cfg := poolTestConfig()
	cfg.MaxAgents = workers
	cfg.MaxConcurrentRuns = workers
	pool := NewSessionPool(cfg, bridge, SessionPoolOpts{})
	defer func() { require.NoError(t, pool.Close(context.Background())) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errs := make([]error, workers)
	leases := make([]*AgentLease, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := testAgentKey(fmt.Sprintf("conc-%d", i))
			lease, err := pool.PrepareSend(ctx, PrepareSendInput{
				Key:          key,
				Create:       createParamsFor(key),
				View:         view(1, fmt.Sprintf("h%d", i), "", fmt.Sprintf("t%d", i)),
				FullPrompt:   "FULL",
				SuffixPrompt: "SUF",
			})
			errs[i] = err
			leases[i] = lease
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "worker %d PrepareSend", i)
		require.NotNil(t, leases[i], "worker %d lease", i)
	}
	for _, lease := range leases {
		finishTurn(t, pool, lease)
	}
	for _, lease := range leases {
		m := pool.Marker(lease.Key)
		require.Equal(t, 1, m.MessageCount)
		require.EqualValues(t, 1, m.ProcessGeneration)
	}
	require.Equal(t, 0, pool.BusyCount())
	require.Equal(t, workers, pool.LiveCount())
	require.Equal(t, int32(workers), bridge.creates.Load())
	require.Equal(t, int32(0), bridge.disposes.Load())
	require.Equal(t, workers, bridge.aliveCount())
}

func TestConcurrency_IdleReapDisposesOnce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	bridge := newFakeAgentBridge(1)
	cfg := poolTestConfig()
	cfg.AgentIdleTimeout = time.Minute
	var nowMu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	pool := NewSessionPool(cfg, bridge, SessionPoolOpts{
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		},
	})
	defer func() { require.NoError(t, pool.Close(context.Background())) }()

	key := testAgentKey("idle-reap")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, lease)
	require.Equal(t, 1, pool.LiveCount())
	require.Equal(t, 1, pool.Marker(key).MessageCount)
	require.Equal(t, int32(1), bridge.creates.Load())
	require.Equal(t, int32(0), bridge.disposes.Load())

	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()
	pool.ReapIdle()

	require.Equal(t, 0, pool.LiveCount())
	require.Equal(t, 0, pool.BusyCount())
	require.Equal(t, HistoryMarker{}, pool.Marker(key))
	require.Equal(t, int32(1), bridge.disposes.Load())
	require.Equal(t, 0, bridge.aliveCount())
}

func TestConcurrency_CancelInvalidateRemovesEntryOnce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { require.NoError(t, pool.Close(context.Background())) }()

	key := testAgentKey("cancel-inv")
	lease, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	pool.CommitSend(lease)
	require.Equal(t, 1, pool.Marker(key).MessageCount)
	require.Equal(t, 1, pool.LiveCount())

	runBridge := newScriptedRunBridge(2)
	stream := NewRunStream(context.Background(), runBridge, lease, pool, RunStreamOpts{
		CancelTimeout: time.Second,
	})
	defer func() { _ = stream.Close() }()

	res := stream.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	require.Equal(t, lipapi.CancelModeProvider, res.Mode)
	require.NoError(t, res.Err)
	require.Equal(t, int32(1), runBridge.cancelN.Load())
	require.Equal(t, 0, pool.LiveCount())
	require.Equal(t, 0, pool.BusyCount())
	require.Equal(t, HistoryMarker{}, pool.Marker(key))
	require.Equal(t, int32(1), bridge.disposes.Load())

	res2 := stream.Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelExplicit})
	require.Equal(t, lipapi.CancelModeProvider, res2.Mode)
	require.Equal(t, int32(1), runBridge.cancelN.Load(), "CancelRun exactly once")
	require.Equal(t, int32(1), bridge.disposes.Load(), "dispose exactly once")
}

func TestConcurrency_GenerationRestartDisposesOldOnce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	bridge := newFakeAgentBridge(1)
	pool := NewSessionPool(poolTestConfig(), bridge, SessionPoolOpts{})
	defer func() { require.NoError(t, pool.Close(context.Background())) }()

	key := testAgentKey("gen-restart")
	l1, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(1, "h1", "", "t1"),
		FullPrompt: "F", SuffixPrompt: "S",
	})
	require.NoError(t, err)
	finishTurn(t, pool, l1)
	oldID := l1.AgentID
	require.Equal(t, int32(1), bridge.creates.Load())
	require.EqualValues(t, 1, pool.Marker(key).ProcessGeneration)

	bridge.setGen(2)
	l2, err := pool.PrepareSend(context.Background(), PrepareSendInput{
		Key: key, Create: createParamsFor(key), View: view(2, "h2", "h1", "t2"),
		FullPrompt: "F2", SuffixPrompt: "S2",
	})
	require.NoError(t, err)
	require.NotEqual(t, oldID, l2.AgentID)
	require.Equal(t, HistoryBootstrap, l2.Mode)
	require.Equal(t, int32(1), bridge.disposes.Load(), "old agent disposed once before new create completes")
	require.Equal(t, int32(2), bridge.creates.Load())

	finishTurn(t, pool, l2)
	m := pool.Marker(key)
	require.EqualValues(t, 2, m.ProcessGeneration)
	require.Equal(t, 2, m.MessageCount)
	require.Equal(t, 1, pool.LiveCount())
	require.Equal(t, int32(1), bridge.disposes.Load())
}

func TestConcurrency_ShutdownCloseIdempotentDisposesOnce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const n = 3
	bridge := newFakeAgentBridge(1)
	cfg := poolTestConfig()
	cfg.MaxAgents = n
	cfg.MaxConcurrentRuns = n
	pool := NewSessionPool(cfg, bridge, SessionPoolOpts{})

	ctx := context.Background()
	for i := range n {
		key := testAgentKey(fmt.Sprintf("shut-%d", i))
		lease, err := pool.PrepareSend(ctx, PrepareSendInput{
			Key: key, Create: createParamsFor(key), View: view(1, fmt.Sprintf("h%d", i), "", "t"),
			FullPrompt: "F", SuffixPrompt: "S",
		})
		require.NoError(t, err)
		finishTurn(t, pool, lease)
	}
	require.Equal(t, n, pool.LiveCount())
	require.Equal(t, int32(n), bridge.creates.Load())
	require.Equal(t, int32(0), bridge.disposes.Load())

	require.NoError(t, pool.Close(ctx))
	require.Equal(t, 0, pool.LiveCount())
	require.Equal(t, int32(n), bridge.disposes.Load())
	require.Equal(t, 0, bridge.aliveCount())

	require.NoError(t, pool.Close(ctx))
	require.Equal(t, int32(n), bridge.disposes.Load(), "second Close must not dispose again")
	require.Equal(t, 0, pool.LiveCount())
}
