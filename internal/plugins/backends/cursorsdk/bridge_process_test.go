package cursorsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig(exe string) Config {
	return Config{
		APIKey:             "secret-api-key-value",
		BridgeExecutable:   exe,
		BridgeEnvAllowlist: PlatformMinimumEnvNames(),
		BridgeStartTimeout: 5 * time.Second,
		CancelTimeout:      time.Second,
		ShutdownTimeout:    2 * time.Second,
		MaxAgents:          4,
		MaxConcurrentRuns:  2,
		SandboxMode:        SandboxOff,
	}
}

type fakeProc struct {
	pid int

	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter

	waitCh    chan error
	waitCount atomic.Int32
	killCount atomic.Int32
	killed    atomic.Bool
}

func newFakeProc(pid int) *fakeProc {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	return &fakeProc{
		pid:     pid,
		stdinR:  stdinR,
		stdinW:  stdinW,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
		waitCh:  make(chan error, 1),
	}
}

func (p *fakeProc) PID() int              { return p.pid }
func (p *fakeProc) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeProc) Stdout() io.ReadCloser { return p.stdoutR }
func (p *fakeProc) Stderr() io.ReadCloser { return p.stderrR }

func (p *fakeProc) Wait() error {
	p.waitCount.Add(1)
	err := <-p.waitCh
	return err
}

func (p *fakeProc) Kill() error {
	p.killCount.Add(1)
	p.killed.Store(true)
	_ = p.stdinW.Close()
	_ = p.stdoutW.Close()
	_ = p.stderrW.Close()
	select {
	case p.waitCh <- errors.New("killed"):
	default:
	}
	return nil
}

func (p *fakeProc) writeStdoutLine(line string) {
	_, _ = p.stdoutW.Write([]byte(line + "\n"))
}

func (p *fakeProc) writeStderr(s string) {
	_, _ = p.stderrW.Write([]byte(s))
}

func (p *fakeProc) exit(err error) {
	_ = p.stdoutW.Close()
	_ = p.stderrW.Close()
	select {
	case p.waitCh <- err:
	default:
	}
}

func (b *bridgeProcess) testKillCurrentWithoutClose() {
	b.mu.Lock()
	proc := b.proc
	b.mu.Unlock()
	if proc == nil {
		return
	}
	if fp, ok := proc.(*fakeProc); ok {
		fp.exit(errors.New("simulated exit"))
		return
	}
	if cp, ok := proc.(*countingProc); ok {
		cp.exit(errors.New("simulated exit"))
		return
	}
	_ = proc.Kill()
}

type startRec struct {
	cmd []string
	cwd string
	env []string
}

type recordingStarter struct {
	mu     sync.Mutex
	starts []startRec
	next   func(cmd []string, cwd string, env []string) (Process, error)
}

func (s *recordingStarter) Start(cmd []string, cwd string, env []string) (Process, error) {
	s.mu.Lock()
	s.starts = append(s.starts, startRec{
		cmd: append([]string(nil), cmd...),
		cwd: cwd,
		env: append([]string(nil), env...),
	})
	fn := s.next
	s.mu.Unlock()
	if fn == nil {
		return nil, errors.New("no starter next")
	}
	return fn(cmd, cwd, env)
}

func (s *recordingStarter) last() startRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts[len(s.starts)-1]
}

func (s *recordingStarter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.starts)
}

func respondInitialize(t *testing.T, p *fakeProc) {
	t.Helper()
	go func() {
		line, err := readPipeLine(p.stdinR)
		if err != nil {
			return
		}
		f, err := protocol.DecodeLine([]byte(line))
		if err != nil || f.Method != protocol.MethodInitialize {
			return
		}
		res, _ := json.Marshal(protocol.InitializeResult{
			SchemaVersion:    protocol.SchemaVersion,
			ImplVersion:      "fake-1.0.0",
			SDKVersion:       protocol.PinnedSDKVersion,
			NodeVersion:      "22.13.0",
			Capabilities:     protocol.RequiredMethods(),
			SandboxSupported: true,
		})
		p.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeResponse,
			ID:            f.ID,
			Method:        protocol.MethodInitialize,
			Result:        res,
		}))
	}()
}

func readPipeLine(r *io.PipeReader) (string, error) {
	var buf []byte
	tmp := make([]byte, 256)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if line, _, ok := bytes.Cut(buf, []byte{'\n'}); ok {
				return strings.TrimSpace(string(line)), nil
			}
		}
		if err != nil {
			if len(buf) > 0 {
				return strings.TrimSpace(string(buf)), err
			}
			return "", err
		}
	}
}

func mustEncodeFrame(f *protocol.Frame) string {
	raw, err := protocol.EncodeFrame(f)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestBridgeProcess_StartupSingleflight(t *testing.T) {
	t.Parallel()
	var started atomic.Int32
	gate := make(chan struct{})
	proc := newFakeProc(910001)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		started.Add(1)
		<-gate
		respondInitialize(t, proc)
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{
		Starter: starter,
		HostEnv: []string{"PATH=/bin", "CURSOR_API_KEY=should-not-pass", "HOME=/tmp"},
	})
	defer func() { _ = bp.Close() }()

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	infos := make([]BridgeInfo, 3)
	for i := range 3 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			infos[i], errs[i] = bp.EnsureReady(ctx)
		}(i)
	}
	require.Eventually(t, func() bool { return started.Load() == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, starter.count())
	close(gate)
	wg.Wait()
	for i := range 3 {
		require.NoError(t, errs[i])
		assert.Equal(t, protocol.SchemaVersion, infos[i].SchemaVersion)
	}
	assert.Equal(t, 1, starter.count())
	assert.EqualValues(t, 1, bp.Generation())
}

func TestBridgeProcess_DirectExecEnvNoAPIKey(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910002)
	respondInitialize(t, proc)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		return proc, nil
	}}
	cfg := testConfig(`C:\tools\lip-cursor-sdk-bridge.exe`)
	cfg.BridgeEnvAllowlist = append(PlatformMinimumEnvNames(), "CUSTOM_OK")
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: starter,
		HostEnv: []string{
			"PATH=C:\\Windows\\System32",
			"CURSOR_API_KEY=secret-api-key-value",
			"OPENAI_API_KEY=other",
			"CUSTOM_OK=yes",
			"SECRET_TOKEN=nope",
			"HOME=/tmp",
		},
	})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	rec := starter.last()
	require.Equal(t, []string{cfg.BridgeExecutable}, rec.cmd)
	joined := strings.Join(rec.env, "\n")
	assert.NotContains(t, joined, "CURSOR_API_KEY")
	assert.NotContains(t, joined, "secret-api-key-value")
	assert.NotContains(t, joined, "OPENAI_API_KEY")
	assert.NotContains(t, joined, "SECRET_TOKEN")
	assert.Contains(t, joined, "CUSTOM_OK=yes")
	assert.Contains(t, joined, "PATH=")
	for _, a := range rec.cmd {
		assert.NotContains(t, a, "secret-api-key-value")
		assert.NotContains(t, strings.ToLower(a), "api_key")
	}
}

func TestBridgeProcess_IncompatibleVersion(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910003)
	go func() {
		line, err := readPipeLine(proc.stdinR)
		if err != nil {
			return
		}
		f, err := protocol.DecodeLine([]byte(line))
		if err != nil {
			return
		}
		proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeResponse,
			ID:            f.ID,
			Method:        protocol.MethodInitialize,
			Error:         &protocol.ErrorBody{Code: protocol.ErrorIncompatibleVersion, Message: "too new"},
		}))
	}()
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()

	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, protocol.ErrorIncompatibleVersion)
}

func TestBridgeProcess_IncompatibleSchemaResult(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910012)
	go func() {
		line, err := readPipeLine(proc.stdinR)
		if err != nil {
			return
		}
		f, err := protocol.DecodeLine([]byte(line))
		if err != nil {
			return
		}
		res, _ := json.Marshal(map[string]any{
			"schemaVersion": 999,
			"implVersion":   "x",
			"sdkVersion":    protocol.PinnedSDKVersion,
			"nodeVersion":   "22.13.0",
			"capabilities":  []string{},
		})
		proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeResponse,
			ID:            f.ID,
			Method:        protocol.MethodInitialize,
			Result:        res,
		}))
	}()
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, protocol.ErrorIncompatibleVersion)
}

func TestBridgeProcess_CallCorrelationAndConcurrent(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910004)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, nil)
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	ctx := context.Background()
	var wg sync.WaitGroup
	const n = 8
	results := make([]*protocol.Frame, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = bp.Call(ctx, protocol.MethodHealth, json.RawMessage(`{}`))
		}(i)
	}
	wg.Wait()
	ids := map[string]struct{}{}
	for i := range n {
		require.NoError(t, errs[i], "i=%d", i)
		require.NotNil(t, results[i])
		assert.Equal(t, protocol.TypeResponse, results[i].Type)
		ids[results[i].ID] = struct{}{}
		var hr protocol.HealthResult
		require.NoError(t, json.Unmarshal(results[i].Result, &hr))
		assert.True(t, hr.OK)
	}
	assert.Len(t, ids, n)
}

func TestBridgeProcess_RunEventDemuxFoundation(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910005)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-A"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				seq1, seq2 := int64(1), int64(2)
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-A",
					Seq:           &seq1,
					Kind:          protocol.KindTextDelta,
					Payload:       json.RawMessage(`{"text":"hi"}`),
				}))
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-B",
					Seq:           &seq1,
					Kind:          protocol.KindTextDelta,
					Payload:       json.RawMessage(`{"text":"other"}`),
				}))
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-A",
					Seq:           &seq2,
					Kind:          protocol.KindFinished,
					Payload:       json.RawMessage(`{}`),
				}))
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	ch, cancel, _ := bp.SubscribeRun("run-A")
	defer cancel()
	resp, err := bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"p"}`))
	require.NoError(t, err)
	var send protocol.AgentSendResult
	require.NoError(t, json.Unmarshal(resp.Result, &send))
	require.Equal(t, "run-A", send.RunID)

	var got []*protocol.Frame
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case f, ok := <-ch:
			require.True(t, ok)
			got = append(got, f)
		case <-deadline:
			t.Fatal("timeout waiting for run events")
		}
	}
	require.Len(t, got, 2)
	assert.Equal(t, "run-A", got[0].RunID)
	assert.EqualValues(t, 1, *got[0].Seq)
	assert.Equal(t, protocol.KindTextDelta, got[0].Kind)
	assert.EqualValues(t, 2, *got[1].Seq)
	assert.Equal(t, protocol.KindFinished, got[1].Kind)
}

func TestBridgeProcess_SubscribeAfterSendStillReceivesEvents(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910015)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-late"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				seq1, seq2 := int64(1), int64(2)
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-late",
					Seq:           &seq1,
					Kind:          protocol.KindTextDelta,
					Payload:       json.RawMessage(`{"text":"hi"}`),
				}))
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-late",
					Seq:           &seq2,
					Kind:          protocol.KindFinished,
					Payload:       json.RawMessage(`{}`),
				}))
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	resp, err := bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"p"}`))
	require.NoError(t, err)
	var send protocol.AgentSendResult
	require.NoError(t, json.Unmarshal(resp.Result, &send))
	require.Equal(t, "run-late", send.RunID)

	ch, cancel, _ := bp.SubscribeRun(send.RunID)
	defer cancel()

	var got []*protocol.Frame
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case f, ok := <-ch:
			require.True(t, ok)
			got = append(got, f)
		case <-deadline:
			t.Fatal("timeout waiting for buffered run events after late SubscribeRun")
		}
	}
	require.Len(t, got, 2)
	assert.Equal(t, protocol.KindTextDelta, got[0].Kind)
	assert.Equal(t, protocol.KindFinished, got[1].Kind)
}

func TestBridgeProcess_StaleClosedRunIDReplacedOnSecondSend(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910016)
	sendN := 0
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				sendN++
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-reuse"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				seq1, seq2 := int64(1), int64(2)
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-reuse",
					Seq:           &seq1,
					Kind:          protocol.KindTextDelta,
					Payload:       json.RawMessage(`{"text":"a"}`),
				}))
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-reuse",
					Seq:           &seq2,
					Kind:          protocol.KindFinished,
					Payload:       json.RawMessage(`{}`),
				}))
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	resp1, err := bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"1"}`))
	require.NoError(t, err)
	var s1 protocol.AgentSendResult
	require.NoError(t, json.Unmarshal(resp1.Result, &s1))
	ch1, cancel1, _ := bp.SubscribeRun(s1.RunID)
	require.Equal(t, protocol.KindFinished, drainRunFrames(t, ch1, 2)[1].Kind)
	cancel1()

	resp2, err := bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"2"}`))
	require.NoError(t, err)
	var s2 protocol.AgentSendResult
	require.NoError(t, json.Unmarshal(resp2.Result, &s2))
	require.Equal(t, "run-reuse", s2.RunID)
	ch2, cancel2, _ := bp.SubscribeRun(s2.RunID)
	defer cancel2()
	got := drainRunFrames(t, ch2, 2)
	require.EqualValues(t, 1, *got[0].Seq)
	require.Equal(t, protocol.KindTextDelta, got[0].Kind)
	require.Equal(t, protocol.KindFinished, got[1].Kind)
	require.Equal(t, 2, sendN)
}

func TestBridgeProcess_PreSubscribePreservedAcrossSend(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910017)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-pre"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				seq1 := int64(1)
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeEvent,
					RunID:         "run-pre",
					Seq:           &seq1,
					Kind:          protocol.KindFinished,
					Payload:       json.RawMessage(`{}`),
				}))
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	ch, cancel, _ := bp.SubscribeRun("run-pre")
	defer cancel()
	_, err = bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"p"}`))
	require.NoError(t, err)
	got := drainRunFrames(t, ch, 1)
	require.Equal(t, protocol.KindFinished, got[0].Kind)
}

func TestBridgeProcess_ConcurrentSameRunIDFailsSecondSend(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910018)
	releaseFirstEvents := make(chan struct{})
	var sendN atomic.Int32
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				n := sendN.Add(1)
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-conflict"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				if n != 1 {
					return
				}
				go func() {
					<-releaseFirstEvents
					seq1 := int64(1)
					proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
						SchemaVersion: protocol.SchemaVersion,
						Type:          protocol.TypeEvent,
						RunID:         "run-conflict",
						Seq:           &seq1,
						Kind:          protocol.KindFinished,
						Payload:       json.RawMessage(`{}`),
					}))
				}()
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	resp1, err := bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"1"}`))
	require.NoError(t, err)
	require.NotNil(t, resp1)
	ch, cancel, _ := bp.SubscribeRun("run-conflict")
	defer cancel()

	_, err = bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"2"}`))
	require.ErrorIs(t, err, errRunIDConflict)

	close(releaseFirstEvents)
	_ = drainRunFrames(t, ch, 1)
}

func TestBridgeProcess_AutoArmOverflowFailsDeterministically(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910019)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodAgentSend: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.AgentSendResult{RunID: "run-ovf"})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				for i := int64(1); i <= 33; i++ {
					seq := i
					proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
						SchemaVersion: protocol.SchemaVersion,
						Type:          protocol.TypeEvent,
						RunID:         "run-ovf",
						Seq:           &seq,
						Kind:          protocol.KindTextDelta,
						Payload:       json.RawMessage(`{"text":"x"}`),
					}))
				}
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	_, err = bp.Call(context.Background(), protocol.MethodAgentSend, json.RawMessage(`{"agentId":"a","prompt":"p"}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return bp.runSubClosed("run-ovf")
	}, time.Second, 5*time.Millisecond)

	ch, cancel, termErr := bp.SubscribeRun("run-ovf")
	defer cancel()
	require.NotNil(t, ch)
	count := 0
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				require.LessOrEqual(t, count, 32)
				require.Greater(t, count, 0)
				err := termErr()
				require.Error(t, err)
				var bf *BridgeFault
				require.True(t, errors.As(err, &bf), "overflow terminal must be BridgeFault, got %T %v", err, err)
				assert.Equal(t, CodeBridgeProtocol, bf.Code)
				assert.False(t, errors.Is(err, ErrBridgeExited))
				return
			}
			count++
		case <-deadline:
			t.Fatalf("SubscribeRun after overflow hung after %d frames", count)
		}
	}
}

func drainRunFrames(t *testing.T, ch <-chan *protocol.Frame, n int) []*protocol.Frame {
	t.Helper()
	var got []*protocol.Frame
	deadline := time.After(2 * time.Second)
	for len(got) < n {
		select {
		case f, ok := <-ch:
			require.True(t, ok)
			got = append(got, f)
		case <-deadline:
			t.Fatalf("timeout waiting for %d run frames, got %d", n, len(got))
		}
	}
	return got
}

func TestBridgeProcess_UnexpectedExitFailsPending(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910006)
	blockHealth := make(chan struct{})
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodHealth: func(req *protocol.Frame) {
				<-blockHealth
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		_, err := bp.Call(context.Background(), protocol.MethodHealth, json.RawMessage(`{}`))
		errCh <- err
	}()
	require.Eventually(t, func() bool { return bp.pendingCount() > 0 }, time.Second, 5*time.Millisecond)
	proc.exit(errors.New("exit 1"))
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.ErrorContains(t, err, "bridge")
	case <-time.After(2 * time.Second):
		t.Fatal("pending call did not fail")
	}
	close(blockHealth)
}

func TestBridgeProcess_GenerationInvalidationAndLaterRestart(t *testing.T) {
	t.Parallel()
	var pid atomic.Int32
	pid.Store(920000)
	starter := &recordingStarter{}
	starter.next = func(cmd []string, cwd string, env []string) (Process, error) {
		p := newFakeProc(int(pid.Add(1)))
		go serveFakeBridgeRPC(t, p, nil)
		return p, nil
	}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()

	info1, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	gen1 := bp.Generation()
	require.EqualValues(t, 1, gen1)
	require.Equal(t, gen1, info1.Generation)

	// Force unexpected exit of current generation.
	bp.testKillCurrentWithoutClose()
	require.Eventually(t, func() bool { return !bp.Ready() }, time.Second, 5*time.Millisecond)

	failed, err := bp.Call(context.Background(), protocol.MethodHealth, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Nil(t, failed)

	info2, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	gen2 := bp.Generation()
	assert.Greater(t, gen2, gen1)
	assert.Equal(t, gen2, info2.Generation)
	assert.Equal(t, 2, starter.count())

	ok, err := bp.Call(context.Background(), protocol.MethodHealth, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.NotNil(t, ok)
}

func TestBridgeProcess_GracefulShutdownThenHardKill(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910007)
	ignoreShutdown := true
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodBridgeShutdown: func(req *protocol.Frame) {
				if ignoreShutdown {
					return
				}
				res, _ := json.Marshal(protocol.ShutdownResult{Shutdown: true})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				proc.exit(nil)
			},
		})
		return proc, nil
	}}
	cfg := testConfig("/bridge/exe")
	cfg.ShutdownTimeout = 100 * time.Millisecond
	bp := newBridgeProcess(cfg, bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)

	err = bp.Close()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, proc.killCount.Load(), int32(1))
	assert.EqualValues(t, 1, proc.waitCount.Load())

	// Idempotent Close.
	require.NoError(t, bp.Close())
	assert.EqualValues(t, 1, proc.waitCount.Load())
}

func TestBridgeProcess_GracefulShutdownReapsOnce(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910008)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodBridgeShutdown: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.ShutdownResult{Shutdown: true})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				proc.exit(nil)
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.NoError(t, bp.Close())
	assert.EqualValues(t, 1, proc.waitCount.Load())
	require.NoError(t, bp.Close())
	assert.EqualValues(t, 1, proc.waitCount.Load())
}

func TestBridgeProcess_StartupContextCancel(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		<-gate
		return nil, errors.New("should not start")
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := bp.EnsureReady(ctx); errCh <- err }()
	require.Eventually(t, func() bool { return starter.count() == 1 || ctx.Err() != nil }, 200*time.Millisecond, 5*time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("EnsureReady did not observe cancel")
	}
	close(gate)
}

func TestBridgeProcess_DelayedKillSkipsWrongGeneration(t *testing.T) {
	t.Parallel()
	var procs []*fakeProc
	var mu sync.Mutex
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		p := newFakeProc(930000 + len(procs))
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
	id1 := bp.currentIdentity()

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
	_ = bp.killGenerationIfCurrent(gen1, id1)
	assert.Equal(t, killsBefore, p2.killCount.Load(), "delayed kill must not target newer generation")
}

func TestBridgeProcess_BoundedStderrSanitized(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910009)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go func() {
			respondInitializeSync(t, proc)
			big := strings.Repeat("x", MaxStderrRetainBytes+2048)
			proc.writeStderr("prefix\x00\x01" + big + "secret-api-key-value")
		}()
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		s := bp.RetainedStderr()
		return len(s) > 0 && !strings.Contains(s, "secret-api-key-value")
	}, time.Second, 5*time.Millisecond)
	s := bp.RetainedStderr()
	assert.LessOrEqual(t, len(s), MaxStderrRetainBytes)
	assert.NotContains(t, s, "\x00")
	assert.NotContains(t, s, "secret-api-key-value")
}

func TestBridgeProcess_OversizedStdoutFrameFailsPending(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(910010)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		go serveFakeBridgeRPC(t, proc, map[string]func(req *protocol.Frame){
			protocol.MethodHealth: func(req *protocol.Frame) {
				// Emit an oversized line (protocol reader must reject).
				proc.writeStdoutLine(`{"schemaVersion":1,"type":"response","id":"` + req.ID + `","method":"bridge/health","result":{"ok":true,"pad":"` + strings.Repeat("a", protocol.MaxFrameBytes) + `"}}`)
			},
		})
		return proc, nil
	}}
	bp := newBridgeProcess(testConfig("/bridge/exe"), bridgeOpts{Starter: starter, HostEnv: []string{"PATH=/bin"}})
	defer func() { _ = bp.Close() }()
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	_, err = bp.Call(context.Background(), protocol.MethodHealth, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.ErrorContains(t, err, protocol.ErrorFrameTooLarge)
}

func TestBridgeProcess_WindowsScriptEnvAllowlist(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows script fixture")
	}
	t.Parallel()
	dir := t.TempDir()
	script := filepath.Join(dir, "envprobe.cmd")
	require.NoError(t, os.WriteFile(script, []byte("@echo off\r\nset > \"%~dp0env.txt\"\r\n"), 0o644))

	proc := newFakeProc(910011)
	starter := &recordingStarter{next: func(cmd []string, cwd string, env []string) (Process, error) {
		require.Equal(t, filepath.Clean(script), filepath.Clean(cmd[0]))
		require.Len(t, cmd, 1)
		for _, e := range env {
			require.False(t, strings.HasPrefix(strings.ToUpper(e), "CURSOR_API_KEY="))
			require.NotContains(t, e, "secret-api-key-value")
		}
		go serveFakeBridgeRPC(t, proc, map[string]func(*protocol.Frame){
			protocol.MethodBridgeShutdown: func(req *protocol.Frame) {
				res, _ := json.Marshal(protocol.ShutdownResult{Shutdown: true})
				proc.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            req.ID,
					Method:        req.Method,
					Result:        res,
				}))
				proc.exit(nil)
			},
		})
		return proc, nil
	}}
	cfg := testConfig(script)
	bp := newBridgeProcess(cfg, bridgeOpts{
		Starter: starter,
		HostEnv: []string{"PATH=C:\\Windows\\System32", "CURSOR_API_KEY=secret-api-key-value", "SYSTEMROOT=C:\\Windows"},
	})
	_, err := bp.EnsureReady(context.Background())
	require.NoError(t, err)
	require.NoError(t, bp.Close())
}

func respondInitializeSync(t *testing.T, p *fakeProc) {
	t.Helper()
	line, err := readPipeLine(p.stdinR)
	if err != nil {
		return
	}
	f, err := protocol.DecodeLine([]byte(line))
	if err != nil {
		return
	}
	res, _ := json.Marshal(protocol.InitializeResult{
		SchemaVersion:    protocol.SchemaVersion,
		ImplVersion:      "fake-1.0.0",
		SDKVersion:       protocol.PinnedSDKVersion,
		NodeVersion:      "22.13.0",
		Capabilities:     protocol.RequiredMethods(),
		SandboxSupported: true,
	})
	p.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeResponse,
		ID:            f.ID,
		Method:        protocol.MethodInitialize,
		Result:        res,
	}))
}

func serveFakeBridgeRPC(t *testing.T, p *fakeProc, handlers map[string]func(*protocol.Frame)) {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := p.stdinR.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		for {
			idx := strings.IndexByte(string(buf), '\n')
			if idx < 0 {
				break
			}
			line := strings.TrimSpace(string(buf[:idx]))
			buf = buf[idx+1:]
			if line == "" {
				continue
			}
			f, decErr := protocol.DecodeLine([]byte(line))
			if decErr != nil {
				continue
			}
			switch f.Method {
			case protocol.MethodInitialize:
				res, _ := json.Marshal(protocol.InitializeResult{
					SchemaVersion:    protocol.SchemaVersion,
					ImplVersion:      "fake-1.0.0",
					SDKVersion:       protocol.PinnedSDKVersion,
					NodeVersion:      "22.13.0",
					Capabilities:     protocol.RequiredMethods(),
					SandboxSupported: true,
				})
				p.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            f.ID,
					Method:        f.Method,
					Result:        res,
				}))
			case protocol.MethodHealth:
				if h := handlers[protocol.MethodHealth]; h != nil {
					h(f)
					continue
				}
				res, _ := json.Marshal(protocol.HealthResult{OK: true, Generation: 1})
				p.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            f.ID,
					Method:        f.Method,
					Result:        res,
				}))
			default:
				if h := handlers[f.Method]; h != nil {
					h(f)
					continue
				}
				p.writeStdoutLine(mustEncodeFrame(&protocol.Frame{
					SchemaVersion: protocol.SchemaVersion,
					Type:          protocol.TypeResponse,
					ID:            f.ID,
					Method:        f.Method,
					Result:        json.RawMessage(`{}`),
				}))
			}
		}
		if err != nil {
			return
		}
	}
}
