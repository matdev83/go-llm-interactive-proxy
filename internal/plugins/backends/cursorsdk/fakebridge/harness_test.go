package fakebridge_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/fakebridge"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHarnessInitializeHealthModelsAndShutdown(t *testing.T) {
	t.Parallel()
	h := fakebridge.New(fakebridge.DefaultScript())
	var in, out bytes.Buffer
	writeReq(&in, "1", protocol.MethodInitialize, `{"implVersion":"test"}`)
	writeReq(&in, "2", protocol.MethodHealth, `{}`)
	writeReq(&in, "3", protocol.MethodModelsList, `{"apiKey":"REDACTED"}`)
	writeReq(&in, "4", protocol.MethodBridgeShutdown, `{}`)

	require.NoError(t, h.Run(&in, &out))
	frames := decodeAll(t, out.Bytes())
	require.Len(t, frames, 4)
	require.Equal(t, protocol.TypeResponse, frames[0].Type)
	require.Equal(t, "1", frames[0].ID)
	require.Contains(t, string(frames[0].Result), protocol.PinnedSDKVersion)
	require.Equal(t, "2", frames[1].ID)
	require.Contains(t, string(frames[2].Result), "gpt-5.3-codex")
	require.Contains(t, string(frames[3].Result), `"shutdown":true`)
}

func TestHarnessDefaultSendUniqueRunAndAgentIDs(t *testing.T) {
	t.Parallel()
	h := fakebridge.New(fakebridge.DefaultScript())
	var in, out bytes.Buffer
	writeReq(&in, "c1", protocol.MethodAgentCreate, `{}`)
	writeReq(&in, "c2", protocol.MethodAgentCreate, `{}`)
	writeReq(&in, "s1", protocol.MethodAgentSend, `{"agentId":"agent-1","prompt":"a"}`)
	writeReq(&in, "s2", protocol.MethodAgentSend, `{"agentId":"agent-2","prompt":"b"}`)
	writeReq(&in, "d1", protocol.MethodBridgeShutdown, `{}`)
	require.NoError(t, h.Run(&in, &out))
	frames := decodeAll(t, out.Bytes())
	require.Contains(t, string(frames[0].Result), `"agentId":"agent-1"`)
	require.Contains(t, string(frames[1].Result), `"agentId":"agent-2"`)
	require.Contains(t, string(frames[2].Result), `"runId":"run-1"`)
	require.Equal(t, "run-1", frames[3].RunID)
	require.Contains(t, string(frames[5].Result), `"runId":"run-2"`)
	require.Equal(t, "run-2", frames[6].RunID)
}

func TestHarnessScriptsEventsAndBlockedCancel(t *testing.T) {
	t.Parallel()
	script := fakebridge.DefaultScript()
	script.OnStartup = []fakebridge.Action{{Type: fakebridge.ActionBlockCancel}}
	script.OnMethod["agent/send"] = []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-1"}`)},
		{Type: fakebridge.ActionEvent, RunID: "run-1", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"hi"}`)},
		{Type: fakebridge.ActionEvent, RunID: "run-1", Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	h := fakebridge.New(script)

	var in, out bytes.Buffer
	writeReq(&in, "s1", protocol.MethodAgentSend, `{"agentId":"agent-1","prompt":"x"}`)
	writeReq(&in, "c1", protocol.MethodRunCancel, `{"runId":"run-1"}`)
	writeReq(&in, "d1", protocol.MethodBridgeShutdown, `{}`)

	require.NoError(t, h.Run(&in, &out))
	frames := decodeAll(t, out.Bytes())
	require.Equal(t, protocol.TypeResponse, frames[0].Type)
	require.Equal(t, protocol.TypeEvent, frames[1].Type)
	require.Equal(t, protocol.KindTextDelta, frames[1].Kind)
	require.Equal(t, protocol.TypeEvent, frames[2].Type)
	require.Equal(t, protocol.KindFinished, frames[2].Kind)
	// cancel blocked => no response for c1; shutdown still answers
	require.Equal(t, "d1", frames[3].ID)
	require.Contains(t, h.StderrText(), "cancel blocked")
}

func TestHarnessMalformedOversizedStderrExit(t *testing.T) {
	t.Parallel()
	script := fakebridge.Script{
		OnStartup: []fakebridge.Action{
			{Type: fakebridge.ActionStderr, Text: "boot"},
			{Type: fakebridge.ActionMalformed, Line: "{bad"},
			{Type: fakebridge.ActionOversized, Bytes: 32},
			{Type: fakebridge.ActionExit, Code: 7},
		},
	}
	h := fakebridge.New(script)
	var out bytes.Buffer
	require.NoError(t, h.Run(strings.NewReader(""), &out))
	require.Equal(t, "boot", h.StderrText())
	code, ok := h.ExitCode()
	require.True(t, ok)
	require.Equal(t, 7, code)
	require.Contains(t, out.String(), "{bad")
	require.Greater(t, len(out.Bytes()), 32)
}

func TestHarnessIgnoreShutdown(t *testing.T) {
	t.Parallel()
	script := fakebridge.DefaultScript()
	script.OnStartup = []fakebridge.Action{{Type: fakebridge.ActionIgnoreShutdown}}
	h := fakebridge.New(script)
	var in, out bytes.Buffer
	writeReq(&in, "1", protocol.MethodBridgeShutdown, `{}`)
	writeReq(&in, "2", protocol.MethodHealth, `{}`)
	require.NoError(t, h.Run(&in, &out))
	frames := decodeAll(t, out.Bytes())
	require.Len(t, frames, 1)
	require.Equal(t, "2", frames[0].ID)
	require.Contains(t, h.StderrText(), "shutdown ignored")
}

func TestHarnessWaitForFileGatesBeforeEvents(t *testing.T) {
	t.Parallel()
	gate := filepath.Join(t.TempDir(), "release")
	script := fakebridge.DefaultScript()
	script.OnMethod[protocol.MethodAgentSend] = []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-gate"}`)},
		{Type: fakebridge.ActionWaitForFile, Path: gate, Ms: 5000},
		{Type: fakebridge.ActionEvent, RunID: "run-gate", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"after"}`)},
		{Type: fakebridge.ActionEvent, RunID: "run-gate", Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	h := fakebridge.New(script)
	var in, out bytes.Buffer
	writeReq(&in, "1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"p"}`)

	done := make(chan error, 1)
	go func() { done <- h.Run(&in, &out) }()

	deadline := time.After(2 * time.Second)
	for {
		frames := decodeAll(t, out.Bytes())
		if len(frames) >= 1 && frames[0].Type == protocol.TypeResponse {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for respond before gate release")
		case err := <-done:
			t.Fatalf("harness finished early: %v", err)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.Len(t, decodeAll(t, out.Bytes()), 1, "events must wait for gate file")
	require.NoError(t, os.WriteFile(gate, []byte("go"), 0o644))
	require.NoError(t, <-done)
	frames := decodeAll(t, out.Bytes())
	require.GreaterOrEqual(t, len(frames), 3)
	require.Equal(t, protocol.TypeEvent, frames[1].Type)
}

func TestHarnessWaitForFileInterruptedByConcurrentCancel(t *testing.T) {
	t.Parallel()
	gate := filepath.Join(t.TempDir(), "never-release")
	script := fakebridge.DefaultScript()
	script.OnMethod[protocol.MethodAgentSend] = []fakebridge.Action{
		{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-slow"}`)},
		{Type: fakebridge.ActionWaitForFile, Path: gate, Ms: 20000},
		{Type: fakebridge.ActionEvent, RunID: "run-slow", Seq: 1, Kind: protocol.KindTextDelta, Payload: json.RawMessage(`{"text":"should-not-emit"}`)},
		{Type: fakebridge.ActionEvent, RunID: "run-slow", Seq: 2, Kind: protocol.KindFinished, Payload: json.RawMessage(`{"status":"finished"}`)},
	}
	h := fakebridge.New(script)
	pr, pw := io.Pipe()
	var (
		outMu sync.Mutex
		out   bytes.Buffer
	)
	writer := &lockedWriter{mu: &outMu, buf: &out}
	done := make(chan error, 1)
	go func() { done <- h.Run(pr, writer) }()

	writeReqPipe(t, pw, "1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"p"}`)
	deadline := time.After(2 * time.Second)
	for {
		outMu.Lock()
		frames := decodeAll(t, out.Bytes())
		outMu.Unlock()
		if len(frames) >= 1 && frames[0].Type == protocol.TypeResponse {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for respond before cancel")
		case err := <-done:
			t.Fatalf("harness finished early: %v", err)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	writeReqPipe(t, pw, "c1", protocol.MethodRunCancel, `{"runId":"run-slow"}`)
	_ = pw.Close()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForFile must unblock on concurrent run/cancel (not wait for gate timeout)")
	}

	outMu.Lock()
	frames := decodeAll(t, out.Bytes())
	outMu.Unlock()
	var sawCancelResp, sawCancelledTerminal, sawTextDelta bool
	for _, f := range frames {
		if f.ID == "c1" && f.Type == protocol.TypeResponse {
			sawCancelResp = true
		}
		if f.Type == protocol.TypeEvent && f.Kind == protocol.KindFinished {
			sawCancelledTerminal = true
		}
		if f.Type == protocol.TypeEvent && f.Kind == protocol.KindTextDelta {
			sawTextDelta = true
		}
	}
	require.True(t, sawCancelResp, "cancel response required")
	require.True(t, sawCancelledTerminal, "cancelled terminal required")
	require.False(t, sawTextDelta, "post-cancel wait_for_file events must be skipped")
}

func TestHarnessCreateCountFile(t *testing.T) {
	t.Parallel()
	countPath := filepath.Join(t.TempDir(), "creates.txt")
	script := fakebridge.DefaultScript()
	script.CreateCountFile = countPath
	h := fakebridge.New(script)
	var in, out bytes.Buffer
	writeReq(&in, "c1", protocol.MethodAgentCreate, `{}`)
	writeReq(&in, "c2", protocol.MethodAgentCreate, `{}`)
	require.NoError(t, h.Run(&in, &out))
	raw, err := os.ReadFile(countPath)
	require.NoError(t, err)
	require.Equal(t, "2\n", string(raw))
}

func TestHarnessOutOfOrderEvents(t *testing.T) {
	t.Parallel()
	script := fakebridge.Script{
		OnMethod: map[string][]fakebridge.Action{
			protocol.MethodAgentSend: {
				{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-1"}`)},
				{Type: fakebridge.ActionOutOfOrderEvents},
			},
		},
	}
	h := fakebridge.New(script)
	var in, out bytes.Buffer
	writeReq(&in, "1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"p"}`)
	require.NoError(t, h.Run(&in, &out))
	frames := decodeAll(t, out.Bytes())
	require.Len(t, frames, 3)
	require.EqualValues(t, 2, *frames[1].Seq)
	require.EqualValues(t, 1, *frames[2].Seq)

	seq := protocol.NewRunSequencer()
	require.NoError(t, seq.Accept(frames[1]))
	err := seq.Accept(frames[2])
	var pe *protocol.ProtocolError
	require.ErrorAs(t, err, &pe)
	require.Equal(t, protocol.ErrorSequenceRegression, pe.Class)
}

func TestHarnessHoldUntilCancelWritesActiveNotifyFile(t *testing.T) {
	t.Parallel()
	notify := filepath.Join(t.TempDir(), "peer-active")
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{
		{
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"$auto"}`)},
			{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID, Path: notify},
		},
	}
	h := fakebridge.New(script)
	var in, out bytes.Buffer
	writeReq(&in, "s1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"hold"}`)
	writeReq(&in, "d1", protocol.MethodBridgeShutdown, `{}`)
	require.NoError(t, h.Run(&in, &out))
	raw, err := os.ReadFile(notify)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "active")
}

func TestHarnessAutoRunIDHoldUntilCancel(t *testing.T) {
	t.Parallel()
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{
		{
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"$auto"}`)},
			{Type: fakebridge.ActionHoldUntilCancel, RunID: fakebridge.AutoRunID},
		},
	}
	h := fakebridge.New(script)
	pr, pw := io.Pipe()
	var (
		outMu sync.Mutex
		out   bytes.Buffer
	)
	done := make(chan error, 1)
	go func() { done <- h.Run(pr, &lockedWriter{mu: &outMu, buf: &out}) }()
	writeReqPipe(t, pw, "s1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"hold"}`)
	waitForResponseID(t, &outMu, &out, done, "s1")
	writeReqPipe(t, pw, "c1", protocol.MethodRunCancel, `{"runId":"run-1"}`)
	writeReqPipe(t, pw, "d1", protocol.MethodBridgeShutdown, `{}`)
	_ = pw.Close()
	require.NoError(t, <-done)
	outMu.Lock()
	frames := decodeAll(t, out.Bytes())
	outMu.Unlock()
	require.Contains(t, string(frames[0].Result), `"runId":"run-1"`)
	require.Equal(t, protocol.KindFinished, frames[2].Kind)
	require.Equal(t, "run-1", frames[2].RunID)
	require.Contains(t, string(frames[2].Payload), `"status":"cancelled"`)
}

func TestHarnessHoldUntilCancelEmitsSingleCancelledTerminal(t *testing.T) {
	t.Parallel()
	script := fakebridge.DefaultScript()
	script.OnAgentSend = [][]fakebridge.Action{
		{
			{Type: fakebridge.ActionRespond, Result: json.RawMessage(`{"runId":"run-hold"}`)},
			{Type: fakebridge.ActionHoldUntilCancel, RunID: "run-hold"},
		},
	}
	h := fakebridge.New(script)
	pr, pw := io.Pipe()
	var (
		outMu sync.Mutex
		out   bytes.Buffer
	)
	done := make(chan error, 1)
	go func() { done <- h.Run(pr, &lockedWriter{mu: &outMu, buf: &out}) }()
	writeReqPipe(t, pw, "s1", protocol.MethodAgentSend, `{"agentId":"a","prompt":"hold"}`)
	waitForResponseID(t, &outMu, &out, done, "s1")
	writeReqPipe(t, pw, "c1", protocol.MethodRunCancel, `{"runId":"run-hold"}`)
	writeReqPipe(t, pw, "c2", protocol.MethodRunCancel, `{"runId":"run-hold"}`)
	writeReqPipe(t, pw, "d1", protocol.MethodBridgeShutdown, `{}`)
	_ = pw.Close()
	require.NoError(t, <-done)
	outMu.Lock()
	frames := decodeAll(t, out.Bytes())
	outMu.Unlock()
	require.Equal(t, protocol.TypeResponse, frames[0].Type)
	require.Contains(t, string(frames[0].Result), `"runId":"run-hold"`)
	require.Equal(t, "c1", frames[1].ID)
	require.Contains(t, string(frames[1].Result), `"cancelled":true`)
	require.Equal(t, protocol.TypeEvent, frames[2].Type)
	require.Equal(t, protocol.KindFinished, frames[2].Kind)
	require.Equal(t, "run-hold", frames[2].RunID)
	require.Contains(t, string(frames[2].Payload), `"status":"cancelled"`)
	require.Equal(t, "c2", frames[3].ID)
	require.Contains(t, string(frames[3].Result), `"cancelled":true`)
	var finished int
	for _, f := range frames {
		if f.Type == protocol.TypeEvent && f.Kind == protocol.KindFinished {
			finished++
			require.Contains(t, string(f.Payload), `"status":"cancelled"`)
		}
		if f.Type == protocol.TypeEvent && f.Kind == protocol.KindTextDelta {
			t.Fatalf("unexpected content event after hold/cancel path")
		}
	}
	require.Equal(t, 1, finished)
}

func writeReq(buf *bytes.Buffer, id, method, params string) {
	f := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            id,
		Method:        method,
		Params:        json.RawMessage(params),
	}
	raw, err := protocol.EncodeFrame(f)
	if err != nil {
		panic(err)
	}
	buf.Write(raw)
	buf.WriteByte('\n')
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func writeReqPipe(t *testing.T, w io.Writer, id, method, params string) {
	t.Helper()
	f := &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeRequest,
		ID:            id,
		Method:        method,
		Params:        json.RawMessage(params),
	}
	raw, err := protocol.EncodeFrame(f)
	require.NoError(t, err)
	_, err = w.Write(append(raw, '\n'))
	require.NoError(t, err)
}

func waitForResponseID(t *testing.T, mu *sync.Mutex, out *bytes.Buffer, done <-chan error, id string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		frames := decodeAll(t, out.Bytes())
		mu.Unlock()
		for _, f := range frames {
			if f.Type == protocol.TypeResponse && f.ID == id {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for response id %q", id)
		case err := <-done:
			t.Fatalf("harness finished early: %v", err)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func decodeAll(t *testing.T, raw []byte) []*protocol.Frame {
	t.Helper()
	lines := bytes.Split(raw, []byte("\n"))
	var frames []*protocol.Frame
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' || !json.Valid(line) {
			continue
		}
		f, err := protocol.DecodeLine(line)
		if err != nil {
			// oversized/malformed scripted lines are expected in some tests
			continue
		}
		frames = append(frames, f)
	}
	return frames
}
