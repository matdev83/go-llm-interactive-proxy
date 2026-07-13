package codexappserver_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/codexappserver"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// --- fakeProcess: in-memory pipe implementation of acp.Process ---

type fakeProcess struct {
	pid     int
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter
	mu      sync.Mutex
	killed  bool

	// stdinScanner is a persistent scanner over stdinR so sequential reads
	// don't lose buffered data between calls.
	stdinScanner *bufio.Scanner
	stdinOnce    sync.Once
}

var nextFakePID atomic.Int64

func newFakeProcess(t *testing.T) *fakeProcess {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	return &fakeProcess{
		pid:     int(nextFakePID.Add(1)),
		stdinR:  stdinR,
		stdinW:  stdinW,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
	}
}

func (p *fakeProcess) PID() int              { return p.pid }
func (p *fakeProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *fakeProcess) Stdout() io.ReadCloser { return p.stdoutR }
func (p *fakeProcess) Stderr() io.ReadCloser { return p.stderrR }

func (p *fakeProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return nil
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	_ = p.stdinW.Close()
	_ = p.stdoutW.Close()
	_ = p.stderrW.Close()
	return nil
}

func (p *fakeProcess) writeStdout(line string) {
	_, _ = p.stdoutW.Write([]byte(line + "\n"))
}

// readStdinLine reads one line from stdin using a persistent scanner.
func (p *fakeProcess) readStdinLine() (string, error) {
	p.stdinOnce.Do(func() {
		p.stdinScanner = bufio.NewScanner(p.stdinR)
		p.stdinScanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	})
	if !p.stdinScanner.Scan() {
		if err := p.stdinScanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return p.stdinScanner.Text(), nil
}

// --- fakeStarter: injects a pre-created fakeProcess ---

type fakeStarter struct {
	proc     *fakeProcess
	procs    []*fakeProcess
	mu       sync.Mutex
	commands [][]string
}

func (f *fakeStarter) Start(cmd []string, _ string, _ []string) (acp.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, append([]string(nil), cmd...))
	if len(f.procs) > 0 {
		proc := f.procs[0]
		f.procs = f.procs[1:]
		return proc, nil
	}
	return f.proc, nil
}

// --- JSON-RPC helpers ---

func readRequest(proc *fakeProcess) (method string, id json.RawMessage, params json.RawMessage, raw string, err error) {
	raw, err = proc.readStdinLine()
	if err != nil {
		return "", nil, nil, "", err
	}
	var parsed struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if jsonErr := json.Unmarshal([]byte(raw), &parsed); jsonErr != nil {
		return "", nil, nil, raw, jsonErr
	}
	return parsed.Method, parsed.ID, parsed.Params, raw, nil
}

func writeResponse(proc *fakeProcess, id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	proc.writeStdout(string(b))
}

func writeNotification(proc *fakeProcess, method string, params any) {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	proc.writeStdout(string(b))
}

func writeTerminalResponse(proc *fakeProcess, id json.RawMessage, turnID string) {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"turnId": turnID},
	})
	proc.writeStdout(string(b))
}

// --- runAgentSimulator: single configurable handshake + stream driver ---

// simulatorOptions configures the fake Codex app-server behavior.
// The handshake (initialize → initialized → thread/start → turn/start) is always
// the same; the hooks customize what happens at each step.
type simulatorOptions struct {
	// threadID is returned in the thread/start response. Defaults to "thread-fake-001".
	threadID string

	// onThreadStartParams is called with the raw params of thread/start.
	// Used by tests that need to verify what the proxy sent (e.g. cwd).
	onThreadStartParams func(params json.RawMessage)

	// threadStartResultNested, when true, makes the simulator respond to
	// thread/start with the nested shape {"thread":{"id": threadID}} instead
	// of the flat shape {"id": threadID}. Both shapes are accepted by
	// runCodexHandshake for compatibility with different Codex CLI versions.
	threadStartResultNested bool

	// onTurnStartParams is called with the raw params of turn/start.
	// Used by tests that need to verify what the proxy sent (e.g. model).
	onTurnStartParams func(params json.RawMessage)

	// midStream is called after turn/start is read but before any notifications
	// are sent. It can send server requests, read responses, or do nothing.
	// If nil, no mid-stream behavior is injected.
	midStream func(t *testing.T, proc *fakeProcess)

	// notifications is the list of (method, params) pairs to send after turn/start
	// (and after midStream if set). Each is sent as a JSON-RPC notification.
	// If nil, defaults to the standard text + reasoning deltas.
	notifications []simulatorNotification
}

type simulatorNotification struct {
	method string
	params any
}

// runAgentSimulator drives the fake Codex app-server through the full lifecycle:
// initialize → initialized → thread/start → turn/start → notifications → turn/completed.
// It replaces the four near-duplicate handshake implementations that previously
// existed as agentSimulator, agentSimulatorWithApproval, and two inline closures.
//
// Do NOT close stdout — closing it triggers lineChannelReader.finish() which can
// race with lines still buffered in the channel. The codexStream terminates
// naturally when it processes turn/completed. proc.Kill() in t.Cleanup closes
// the pipes for goroutine cleanup.
func runAgentSimulator(t *testing.T, proc *fakeProcess, opts simulatorOptions) {
	t.Helper()
	if opts.threadID == "" {
		opts.threadID = "thread-fake-001"
	}
	if opts.notifications == nil {
		opts.notifications = []simulatorNotification{
			{"item/agentMessage/delta", map[string]any{"threadId": opts.threadID, "itemId": "item-1", "delta": "Hello from"}},
			{"item/agentMessage/delta", map[string]any{"threadId": opts.threadID, "itemId": "item-1", "delta": " Codex!"}},
			{"item/reasoning/summaryTextDelta", map[string]any{"threadId": opts.threadID, "itemId": "item-2", "delta": "Thinking about response"}},
		}
	}

	// 1. initialize
	method, id, _, _, err := readRequest(proc)
	if err != nil {
		t.Errorf("agent: read initialize: %v", err)
		return
	}
	if method != "initialize" {
		t.Errorf("agent: expected initialize, got %s", method)
		return
	}
	writeResponse(proc, id, map[string]any{
		"protocolVersion":   1,
		"agentCapabilities": map[string]any{"experimentalApi": true},
		"agentInfo":         map[string]any{"name": "codex-fake", "version": "0.0.1"},
	})

	// 2. initialized (notification, no id)
	method, _, _, _, err = readRequest(proc)
	if err != nil {
		t.Errorf("agent: read initialized: %v", err)
		return
	}
	if method != "initialized" {
		t.Errorf("agent: expected initialized, got %s", method)
		return
	}

	// 3. thread/start — respond with thread ID, optionally capture params.
	method, id, params, _, err := readRequest(proc)
	if err != nil {
		t.Errorf("agent: read thread/start: %v", err)
		return
	}
	if method != "thread/start" {
		t.Errorf("agent: expected thread/start, got %s", method)
		return
	}
	if opts.onThreadStartParams != nil {
		opts.onThreadStartParams(params)
	}
	if opts.threadStartResultNested {
		writeResponse(proc, id, map[string]any{"thread": map[string]any{"id": opts.threadID}})
	} else {
		writeResponse(proc, id, map[string]any{"id": opts.threadID})
	}

	// 4. turn/start — optionally capture params, inject mid-stream, send notifications.
	method, id, params, _, err = readRequest(proc)
	if err != nil {
		t.Errorf("agent: read turn/start: %v", err)
		return
	}
	if method != "turn/start" {
		t.Errorf("agent: expected turn/start, got %s", method)
		return
	}
	if opts.onTurnStartParams != nil {
		opts.onTurnStartParams(params)
	}
	if opts.midStream != nil {
		opts.midStream(t, proc)
	}
	for _, n := range opts.notifications {
		writeNotification(proc, n.method, n.params)
	}

	// Send turn/completed terminal response (id == turn/start id).
	writeTerminalResponse(proc, id, "turn-fake-001")
}

// --- Tests ---

func TestIntegration_codexAppServerStreamingText(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	go runAgentSimulator(t, proc, simulatorOptions{})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-1",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	text := col.Text.String()
	if !strings.Contains(text, "Hello from") {
		t.Fatalf("text missing 'Hello from': %q", text)
	}
	if !strings.Contains(text, "Codex!") {
		t.Fatalf("text missing 'Codex!': %q", text)
	}
}

func TestIntegration_codexAppServerVerbosityChangeRestartsAndReplaysTranscript(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	first := newFakeProcess(t)
	second := newFakeProcess(t)
	t.Cleanup(func() { _ = first.Kill(); _ = second.Kill() })
	starter := &fakeStarter{procs: []*fakeProcess{first, second}}

	go runAgentSimulator(t, first, simulatorOptions{
		threadID:      "thread-low-verbosity",
		notifications: []simulatorNotification{{"item/agentMessage/delta", map[string]any{"delta": "first"}}},
	})
	var replayed string
	var replayMu sync.Mutex
	go runAgentSimulator(t, second, simulatorOptions{
		threadID: "thread-high-verbosity",
		onTurnStartParams: func(params json.RawMessage) {
			var turn struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(params, &turn)
			if len(turn.Input) > 0 {
				replayMu.Lock()
				replayed = turn.Input[0].Text
				replayMu.Unlock()
			}
		},
		notifications: []simulatorNotification{{"item/agentMessage/delta", map[string]any{"delta": "second"}}},
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{Model: "auto", DefaultWorkspace: ws},
	}, starter)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	firstCall := lipapi.Call{
		ID:       "verbosity-first",
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("first question")}}},
		Options:  lipapi.GenerationOptions{Verbosity: lipapi.VerbosityLow},
	}
	es, err := backend.Open(ctx, firstCall, cand)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := lipapi.Collect(ctx, es); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	_ = es.Close()

	secondCall := lipapi.Call{
		ID: "verbosity-second",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("first question")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("first answer")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("second question")}},
		},
		Options: lipapi.GenerationOptions{Verbosity: lipapi.VerbosityHigh},
	}
	es, err = backend.Open(ctx, secondCall, cand)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if _, err := lipapi.Collect(ctx, es); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	_ = es.Close()

	starter.mu.Lock()
	commands := append([][]string(nil), starter.commands...)
	starter.mu.Unlock()
	if len(commands) != 2 {
		t.Fatalf("spawn count = %d, want 2; commands=%v", len(commands), commands)
	}
	joinedFirst := strings.Join(commands[0], " ")
	joinedSecond := strings.Join(commands[1], " ")
	if !strings.Contains(joinedFirst, "model_verbosity=low") || !strings.Contains(joinedSecond, "model_verbosity=high") {
		t.Fatalf("verbosity spawn commands = %v", commands)
	}
	replayMu.Lock()
	gotReplay := replayed
	replayMu.Unlock()
	if !strings.Contains(gotReplay, "first question") || !strings.Contains(gotReplay, "first answer") || !strings.Contains(gotReplay, "second question") {
		t.Fatalf("changed verbosity must replay full transcript, got %q", gotReplay)
	}
}

// TestIntegration_codexAppServerTranscriptPrompt verifies the Codex backend
// uses TranscriptHistoryCoordinator: on a fresh process it serializes the full
// conversation as a single Markdown transcript in the turn/start input (not
// just the last user message), preserving multi-turn context for the agent.
func TestIntegration_codexAppServerTranscriptPrompt(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	var capturedInput string
	var captureMu sync.Mutex

	go runAgentSimulator(t, proc, simulatorOptions{
		threadID: "thread-transcript",
		onTurnStartParams: func(params json.RawMessage) {
			var ts struct {
				Input []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"input"`
			}
			_ = json.Unmarshal(params, &ts)
			if len(ts.Input) > 0 {
				captureMu.Lock()
				capturedInput = ts.Input[0].Text
				captureMu.Unlock()
			}
		},
		notifications: []simulatorNotification{
			{"item/agentMessage/delta", map[string]any{"delta": "ok"}},
		},
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-transcript",
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("first question")}},
			{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{lipapi.TextPart("first answer")}},
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("second question")}},
		},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	if _, err := lipapi.Collect(ctx, es); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	captureMu.Lock()
	got := capturedInput
	captureMu.Unlock()
	if !strings.Contains(got, "Previous Context") {
		t.Fatalf("fresh-process turn/start input should contain serialized transcript with 'Previous Context', got %q", got)
	}
	if !strings.Contains(got, "first question") || !strings.Contains(got, "second question") {
		t.Fatalf("transcript input should contain both user messages, got %q", got)
	}
}

func TestIntegration_codexAppServerReasoningDelta(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	go runAgentSimulator(t, proc, simulatorOptions{})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-reasoning",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("think")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	reasoning := col.Reasoning.String()
	if !strings.Contains(reasoning, "Thinking about response") {
		t.Fatalf("reasoning missing expected text: %q", reasoning)
	}
}

func TestIntegration_codexAppServerApprovalRequest(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	go runAgentSimulator(t, proc, simulatorOptions{
		threadID: "thread-fake-002",
		midStream: func(t *testing.T, proc *fakeProcess) {
			t.Helper()
			// Send a text delta before the approval.
			writeNotification(proc, "item/agentMessage/delta", map[string]any{
				"threadId": "thread-fake-002", "itemId": "item-1", "delta": "Running command",
			})
			// Send a server-initiated approval request.
			b, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      1001,
				"method":  "execCommandApproval",
				"params":  map[string]any{"command": "ls -la"},
			})
			proc.writeStdout(string(b))

			// Read the approval response from stdin.
			approvalResp, err := proc.readStdinLine()
			if err != nil {
				t.Errorf("agent: read approval response: %v", err)
				return
			}
			var ar struct {
				Result struct {
					Decision string `json:"decision"`
				} `json:"result"`
			}
			if jsonErr := json.Unmarshal([]byte(approvalResp), &ar); jsonErr != nil {
				t.Errorf("agent: parse approval response: %v", jsonErr)
				return
			}
			if ar.Result.Decision != "accept" {
				t.Errorf("agent: approval decision = %q, want accept", ar.Result.Decision)
			}
		},
		// No additional notifications — the midStream already sent the text delta.
		notifications: []simulatorNotification{},
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-approval",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("run a command")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	text := col.Text.String()
	if !strings.Contains(text, "Running command") {
		t.Fatalf("text missing 'Running command': %q", text)
	}
}

func TestIntegration_codexAppServerCloseReleasesPool(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	go runAgentSimulator(t, proc, simulatorOptions{})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-close",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("test close")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	_, err = lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	_ = es.Close() // Explicit close to test the pool release path.
}

func TestIntegration_codexAppServerModelOverride(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	var capturedModel string
	var captureMu sync.Mutex

	go runAgentSimulator(t, proc, simulatorOptions{
		threadID: "thread-fake-003",
		onTurnStartParams: func(params json.RawMessage) {
			var tsParams struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(params, &tsParams)
			captureMu.Lock()
			capturedModel = tsParams.Model
			captureMu.Unlock()
		},
		notifications: []simulatorNotification{
			{"item/agentMessage/delta", map[string]any{"delta": "ok"}},
		},
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "gpt-5.4",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-model",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("test")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "gpt-5.4"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	_, err = lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	captureMu.Lock()
	got := capturedModel
	captureMu.Unlock()
	if got != "gpt-5.4" {
		t.Fatalf("turn/start model = %q, want gpt-5.4", got)
	}
}

func TestIntegration_codexAppServerWorkspaceFromExtension(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	var capturedCwd string
	var captureMu sync.Mutex

	go runAgentSimulator(t, proc, simulatorOptions{
		threadID: "thread-fake-004",
		onThreadStartParams: func(params json.RawMessage) {
			var tsParams struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(params, &tsParams)
			captureMu.Lock()
			capturedCwd = tsParams.Cwd
			captureMu.Unlock()
		},
		notifications: []simulatorNotification{
			{"item/agentMessage/delta", map[string]any{"delta": "ok"}},
		},
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: "/default-ws",
		},
	}, starter)

	customWS := t.TempDir()
	wsRaw, _ := json.Marshal(customWS)
	call := lipapi.Call{
		ID: "int-codex-ws",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("test")},
		}},
		Extensions: map[string]json.RawMessage{
			"acp.workspace": wsRaw,
		},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	_, err = lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	captureMu.Lock()
	got := capturedCwd
	captureMu.Unlock()
	if got != customWS {
		t.Fatalf("thread/start cwd = %q, want %s", got, customWS)
	}
}

// TestIntegration_codexAppServerAcceptsNestedThreadStartResult verifies the
// handshake accepts the nested {"thread":{"id": ...}} thread/start response
// shape used by some Codex CLI versions, in addition to the flat {"id": ...}
// shape covered by the other integration tests.
func TestIntegration_codexAppServerAcceptsNestedThreadStartResult(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	go runAgentSimulator(t, proc, simulatorOptions{
		threadID:                "thread-nested-001",
		threadStartResultNested: true,
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-nested-thread",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open with nested thread/start result: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !strings.Contains(col.Text.String(), "Hello from") {
		t.Fatalf("expected streamed text, got %q", col.Text.String())
	}
}

// TestIntegration_codexAppServerItemCompletedSummary verifies the end-to-end
// path that the reviewer flagged: Codex item/completed notifications flow
// through the connector and surface as fenced tool-completion summaries in the
// canonical text stream. It uses the real wire shape (item nested under
// params.item, per the ItemCompletedNotification schema) for both
// commandExecution and fileChange items, and asserts raw command stdout is
// never leaked — only its size is reported.
func TestIntegration_codexAppServerItemCompletedSummary(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	proc := newFakeProcess(t)
	t.Cleanup(func() { _ = proc.Kill() })
	starter := &fakeStarter{proc: proc}

	go runAgentSimulator(t, proc, simulatorOptions{
		threadID: "thread-item-completed",
		notifications: []simulatorNotification{
			{"item/agentMessage/delta", map[string]any{"delta": "Running tasks"}},
			// Real Codex wire shape: item nested under params.item (required by schema).
			{"item/completed", map[string]any{
				"threadId": "thread-item-completed",
				"turnId":   "turn-fake-001",
				"item": map[string]any{
					"type":             "commandExecution",
					"commandActions":   []any{map[string]any{"command": "/usr/bin/git status"}},
					"durationMs":       float64(120),
					"aggregatedOutput": "clean tree",
				},
			}},
			{"item/completed", map[string]any{
				"threadId": "thread-item-completed",
				"turnId":   "turn-fake-001",
				"item": map[string]any{
					"type": "fileChange",
					"changes": []any{
						map[string]any{"path": "/repo/a.go"},
						map[string]any{"path": "/repo/b.go"},
					},
				},
			}},
		},
	})

	backend := codexappserver.NewWithStarter(codexappserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Model:            "auto",
			DefaultWorkspace: ws,
		},
	}, starter)

	call := lipapi.Call{
		ID: "int-codex-item-completed",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("do work")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: codexappserver.ID, Model: "auto"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	es, err := backend.Open(ctx, call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	col, err := lipapi.Collect(ctx, es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	text := col.Text.String()
	if !strings.Contains(text, "Running tasks") {
		t.Fatalf("text missing streamed delta: %q", text)
	}
	if !strings.Contains(text, "Tool: git\n") {
		t.Fatalf("text missing commandExecution summary: %q", text)
	}
	if !strings.Contains(text, "Tool: fileChange\n") {
		t.Fatalf("text missing fileChange summary: %q", text)
	}
	// Raw command stdout must never be streamed — only the output size is reported.
	if strings.Contains(text, "clean tree") {
		t.Fatalf("raw command output leaked into stream: %q", text)
	}
}
