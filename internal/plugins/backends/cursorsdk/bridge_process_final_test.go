package cursorsdk

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBridgeProcess_CloseDuringBlockedStartReapsLateProcess(t *testing.T) {
	var waitN, killN atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		close(started)
		<-release
		p := newFakeProc(980001)
		return &countingProc{fakeProc: p, waitN: &waitN, killN: &killN}, nil
	}}
	cfg := testConfig("/bridge/exe")
	cfg.BridgeStartTimeout = 2 * time.Second
	cfg.ShutdownTimeout = 2 * time.Second
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter:   starter,
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := bp.EnsureReady(ctx); errCh <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)

	closeDone := make(chan error, 1)
	go func() { closeDone <- bp.Close() }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	require.NoError(t, <-closeDone)
	require.Eventually(t, func() bool { return killN.Load() == 1 && waitN.Load() == 1 }, time.Second, 5*time.Millisecond)
	assert.False(t, bp.Ready())
	assert.True(t, bp.closed.Load())
	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	assert.EqualValues(t, 1, killN.Load())
	assert.EqualValues(t, 1, waitN.Load())
}

func TestBridgeProcess_CloseDuringHandshake(t *testing.T) {
	initStarted := make(chan struct{})
	releaseInit := make(chan struct{})
	proc := newFakeProc(980002)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go func() {
			line, err := readPipeLine(proc.stdinR)
			if err != nil {
				return
			}
			f, err := protocol.DecodeLine([]byte(line))
			if err != nil {
				return
			}
			close(initStarted)
			<-releaseInit
			res, _ := json.Marshal(protocol.InitializeResult{
				SchemaVersion:    protocol.SchemaVersion,
				ImplVersion:      "fake",
				SDKVersion:       protocol.PinnedSDKVersion,
				NodeVersion:      "22.13.0",
				Capabilities:     protocol.RequiredMethods(),
				SandboxSupported: true,
			})
			proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
				SchemaVersion: protocol.SchemaVersion,
				Type:          protocol.TypeResponse,
				ID:            f.ID,
				Method:        f.Method,
				Result:        res,
			}))
		}()
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter:   starter,
		HostEnv:   []string{"PATH=/bin"},
		Inspector: fakeInspector(),
	})
	errCh := make(chan error, 1)
	go func() { _, err := bp.EnsureReady(context.Background()); errCh <- err }()
	<-initStarted
	require.NoError(t, bp.Close())
	close(releaseInit)
	require.Error(t, <-errCh)
	assert.False(t, bp.Ready())
	assert.EqualValues(t, 1, proc.waitCount.Load())
}

func TestBridgeProcess_IdentityMismatchKillStillReapsBounded(t *testing.T) {
	ins := fakeInspector()
	proc := newFakeProc(980003)
	var waitN atomic.Int32
	var killN atomic.Int32
	cp := &countingProc{fakeProc: proc, waitN: &waitN, killN: &killN}
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(*protocol.Frame){
			protocol.MethodBridgeShutdown: func(req *protocol.Frame) {},
		})
		return cp, nil
	}}
	cfg := testConfig("/bridge/exe")
	cfg.ShutdownTimeout = 200 * time.Millisecond
	bp := newBridgeProcess(cfg, bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: ins})
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	id := bp.currentIdentity()
	id.ExeKey = normalizeExeKey("/other/exe")
	bp.mu.Lock()
	bp.identity = id
	bp.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- bp.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked on identity mismatch")
	}
	assert.GreaterOrEqual(t, killN.Load(), int32(1))
	assert.EqualValues(t, 1, waitN.Load())
}

func TestBridgeProcess_DelayedKillIdentityMismatchNoWaitHang(t *testing.T) {
	ins := fakeInspector()
	proc := newFakeProc(980200)
	var killN atomic.Int32
	cp := &countingProc{fakeProc: proc, waitN: &atomic.Int32{}, killN: &killN}
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, nil)
		return cp, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: ins})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	gen := bp.Generation()
	id := bp.currentIdentity()
	id.ExeKey = normalizeExeKey("/other/exe")
	before := killN.Load()
	done := make(chan error, 1)
	go func() { done <- bp.killGenerationIfCurrent(gen, id) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("killGenerationIfCurrent hung on identity mismatch")
	}
	assert.Equal(t, before, killN.Load())
}
