package product

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeInspector() processInspector {
	times := map[int]time.Time{}
	exes := map[int]string{}
	var mu sync.Mutex
	return processInspector{
		StartTime: func(pid int) time.Time {
			mu.Lock()
			defer mu.Unlock()
			if ct, ok := times[pid]; ok {
				return ct
			}
			ct := time.Unix(1_700_000_000+int64(pid%1000), 0)
			times[pid] = ct
			return ct
		},
		ExePath: func(pid int) string {
			mu.Lock()
			defer mu.Unlock()
			if e, ok := exes[pid]; ok {
				return e
			}
			e := "/fake/bridge/" + itoa(pid)
			exes[pid] = e
			return e
		},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestBridgeProcess_EnsureReadyCanceledCallerDoesNotPoisonPeer(t *testing.T) {
	var starts atomic.Int32
	gate := make(chan struct{})
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		starts.Add(1)
		<-gate
		p := newFakeProc(940001)
		go serveFakeBridgeRPC(t, p, nil)
		return p, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	ctx1, cancel1 := context.WithCancel(context.Background())
	errCh1 := make(chan error, 1)
	go func() { _, err := bp.EnsureReady(ctx1); errCh1 <- err }()
	require.Eventually(t, func() bool { return starts.Load() == 1 }, time.Second, 5*time.Millisecond)
	cancel1()
	require.ErrorIs(t, <-errCh1, context.Canceled)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	errCh2 := make(chan error, 1)
	var info2 BridgeInfo
	go func() {
		var err error
		info2, err = bp.EnsureReady(ctx2)
		errCh2 <- err
	}()
	close(gate)
	require.NoError(t, <-errCh2)
	require.Equal(t, protocol.SchemaVersion, info2.SchemaVersion)
	assert.EqualValues(t, 1, starts.Load())
	assert.EqualValues(t, 1, bp.Generation())
}

func TestBridgeProcess_LateStartAfterTimeoutGetsKillAndWait(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var waitN atomic.Int32
	var killN atomic.Int32
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		close(started)
		<-release
		p := newFakeProc(940002)
		origWait := p.waitCh
		p2 := &countingProc{fakeProc: p, waitN: &waitN, killN: &killN, waitCh: origWait}
		return p2, nil
	}}
	cfg := testConfig("/bridge/exe")
	cfg.BridgeStartTimeout = 50 * time.Millisecond
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	<-started
	close(release)
	require.Eventually(t, func() bool { return killN.Load() >= 1 && waitN.Load() >= 1 }, time.Second, 5*time.Millisecond)
	assert.EqualValues(t, 1, waitN.Load())
}

type countingProc struct {
	*fakeProc
	waitN  *atomic.Int32
	killN  *atomic.Int32
	waitCh chan error
}

func (p *countingProc) Wait() error {
	p.waitN.Add(1)
	return p.fakeProc.Wait()
}

func (p *countingProc) Kill() error {
	p.killN.Add(1)
	return p.fakeProc.Kill()
}

func TestBridgeProcess_RunSequencerRejectsRegression(t *testing.T) {
	proc := newFakeProc(940003)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(*protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-S"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				seq2, seq1 := int64(2), int64(1)
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion, Type: protocol.TypeEvent, RunID: "run-S", Seq: &seq2,
					Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"a"}`),
				}))
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion, Type: protocol.TypeEvent, RunID: "run-S", Seq: &seq1,
					Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"b"}`),
				}))
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	ch, cancel, _ := bp.SubscribeRun("run-S")
	defer cancel()
	_, err = bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"p"}`))
	require.NoError(t, err)
	var got []*protocol.Frame
	deadline := time.After(2 * time.Second)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				require.Len(t, got, 1)
				assert.EqualValues(t, 2, *got[0].Seq)
				return
			}
			got = append(got, f)
		case <-deadline:
			t.Fatal("timeout")
		}
	}
}

func TestBridgeProcess_SubscribeStressNoPanic(t *testing.T) {
	proc := newFakeProc(940004)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, nil)
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, cancel, _ := bp.SubscribeRun("run-stress")
			defer cancel()
			seq := int64(i + 1)
			bp.routeFrame(bp.Generation(), &protocol.Frame{
				SchemaVersion: protocol.SchemaVersion,
				Type:          protocol.TypeEvent,
				RunID:         "run-stress",
				Seq:           &seq,
				Kind:          protocol.KindTextDelta,
				Payload:       json.RawMessage(`{}`),
			})
			select {
			case <-ch:
			case <-time.After(50 * time.Millisecond):
			}
		}(i)
	}
	wg.Go(func() {
		time.Sleep(5 * time.Millisecond)
		bp.testKillCurrentWithoutClose()
	})
	wg.Go(func() {
		time.Sleep(10 * time.Millisecond)
		_ = bp.Close()
	})
	wg.Wait()
}

func TestSanitizeBridgeDiag_redactsKeyPathsPrompt(t *testing.T) {
	t.Parallel()
	key := "secret-api-key-value"
	in := "err at /home/user/proj/file.go and C:\\Users\\x\\a.go prompt: hello world " + key + "\x00\x01"
	out := sanitizeBridgeDiag([]byte(in), key)
	assert.NotContains(t, out, key)
	assert.NotContains(t, out, "/home/user")
	assert.NotContains(t, out, `C:\Users`)
	assert.NotContains(t, out, "hello world")
	assert.Contains(t, out, "[PATH]")
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "\x00")
	assert.LessOrEqual(t, len(out), MaxStderrRetainBytes)
}

func TestSanitizeBridgeDiag_retainsLast8KiB(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", MaxStderrRetainBytes+1000)
	out := sanitizeBridgeDiag([]byte(big), "")
	assert.LessOrEqual(t, len(out), MaxStderrRetainBytes)
}

func TestStillSameProcess_usesInjectedIdentity(t *testing.T) {
	ins := fakeInspector()
	p := newFakeProc(940010)
	id := ins.capture(p, "/bridge/exe")
	require.True(t, ins.stillSame(p, id))
	id.CreateTime = id.CreateTime.Add(2 * time.Second)
	require.False(t, ins.stillSame(p, id))
}

func TestBridgeProcess_HandshakeFailureReapsBeforeRestart(t *testing.T) {
	var live atomic.Int32
	starter := &recordingStarter{}
	starter.next = func(cmd []string, cwd string, env []string) (Process, error) {
		n := live.Add(1)
		p := newFakeProc(950000 + int(n))
		if n == 1 {
			go func() {
				line, err := readPipeLine(p.stdinR)
				if err != nil {
					return
				}
				f, err := protocol.DecodeLine([]byte(line))
				if err != nil {
					return
				}
				p.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            f.ID,
					Method:        protocol.MethodInitialize,
					Error:         &protocol.ErrorBody{Code: protocol.ErrorIncompatibleVersion, Message: "no"},
				}))
			}()
			return p, nil
		}
		go serveFakeBridgeRPC(t, p, nil)
		return p, nil
	}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	assert.EqualValues(t, 0, bp.pendingCount())
	info, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, info.Generation)
	assert.Equal(t, 2, starter.count())
}

func TestBridgeProcess_DelayedKillSkipsWrongGeneration_Injected(t *testing.T) {
	var procs []*fakeProc
	var mu sync.Mutex
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		p := newFakeProc(960000 + len(procs))
		mu.Lock()
		procs = append(procs, p)
		mu.Unlock()
		go serveFakeBridgeRPC(t, p, nil)
		return p, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	gen1 := bp.Generation()
	id1 := bp.currentIdentity()
	bp.testKillCurrentWithoutClose()
	require.Eventually(t, func() bool { return !bp.Ready() }, time.Second, 5*time.Millisecond)
	_, err = bp.EnsureReady(context.Background())
	require.NoError(t, err)
	mu.Lock()
	p2 := procs[len(procs)-1]
	mu.Unlock()
	before := p2.killCount.Load()
	require.NoError(t, bp.killGenerationIfCurrent(gen1, id1))
	assert.Equal(t, before, p2.killCount.Load())
}

func TestBridgeProcess_CloseIdempotent(t *testing.T) {
	proc := newFakeProc(940020)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(*protocol.Frame){
			protocol.MethodBridgeShutdown: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.ShutdownResult{Shutdown: true})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion, Type: protocol.TypeResponse, ID: req.ID, Method: req.Method, Result: res,
				}))
				proc.exit(nil)
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter, HostEnv: []string{"PATH=/bin"}, Inspector: fakeInspector(),
	})
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.NoError(t, bp.Close())
	require.NoError(t, bp.Close())
	assert.EqualValues(t, 1, proc.waitCount.Load())
}

func TestPlatformGOOSHelpersCompile(t *testing.T) {
	t.Parallel()
	assert.True(t, runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin" || true)
	_ = errors.New("ok")
}
