package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// compile-time contract: stdioTransport satisfies Transport.
func TestStdioTransport_ImplementsTransport(t *testing.T) {
	t.Parallel()
	var _ Transport = (*stdioTransport)(nil)
}

// fakeProcess implements Process for tests using in-memory pipes.
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
	waitErr error
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
	return p.waitErr
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

// writeStdout writes a line to the fake process stdout (simulating agent output).
func (p *fakeProcess) writeStdout(line string) {
	_, _ = p.stdoutW.Write([]byte(line + "\n"))
}

func (p *fakeProcess) closeStdout() {
	_ = p.stdoutW.Close()
}

// readStdin reads one line from the fake process stdin (simulating what the agent received).
func (p *fakeProcess) readStdin() (string, error) {
	scanner := bufio.NewScanner(p.stdinR)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return scanner.Text(), nil
}

func TestStdioTransport_CallUnary_WritesRequestAndReturnsMatchedResponse(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Agent goroutine: read the initialize request, respond.
	go func() {
		req, err := proc.readStdin()
		if err != nil {
			return
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal([]byte(req), &parsed); err != nil {
			return
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{"protocolVersion": 1},
		}
		b, _ := json.Marshal(resp)
		proc.writeStdout(string(b))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := tr.CallUnary(ctx, body, 200)
	if err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	if !bytes.Contains(got, []byte(`"protocolVersion"`)) {
		t.Fatalf("response missing protocolVersion: %s", got)
	}
	_ = tr.Close()
}

func TestStdioTransport_CallUnary_IgnoresNotificationsBeforeResponse(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		// Read the request.
		req, err := proc.readStdin()
		if err != nil {
			return
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal([]byte(req), &parsed)

		// Send a session/update notification first.
		notif := map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params":  map[string]any{},
		}
		b, _ := json.Marshal(notif)
		proc.writeStdout(string(b))

		// Then send the response.
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{},
		}
		b, _ = json.Marshal(resp)
		proc.writeStdout(string(b))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := tr.CallUnary(ctx, body, 200)
	if err != nil {
		t.Fatalf("CallUnary: %v", err)
	}
	if !bytes.Contains(got, []byte(`"result"`)) {
		t.Fatalf("expected response with result: %s", got)
	}
	_ = tr.Close()
}

func TestStdioTransport_CallUnary_NotificationNoIDWritesAndReturns(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		// Read the notification.
		_, _ = proc.readStdin()
		proc.closeStdout()
	}()

	body := []byte(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := tr.CallUnary(ctx, body, 204)
	if err != nil {
		t.Fatalf("CallUnary notification: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty body for notification, got %s", got)
	}
	_ = tr.Close()
}

func TestStdioTransport_CallPromptStream_ForwardsLines(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		// Read the prompt request to get the id.
		req, err := proc.readStdin()
		if err != nil {
			return
		}
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal([]byte(req), &parsed)

		// Send a session/update notification.
		notif := map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "s1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "hello"},
				},
			},
		}
		b, _ := json.Marshal(notif)
		proc.writeStdout(string(b))

		// Send terminal response.
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{"stopReason": "end_turn"},
		}
		b, _ = json.Marshal(resp)
		proc.writeStdout(string(b))
	}()

	body := []byte(`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, err := tr.CallPromptStream(ctx, body)
	if err != nil {
		t.Fatalf("CallPromptStream: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// Read all lines from the stream.
	all, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(all), "agent_message_chunk") {
		t.Fatalf("missing notification in stream: %s", all)
	}
	if !strings.Contains(string(all), "end_turn") {
		t.Fatalf("missing terminal response in stream: %s", all)
	}
	_ = tr.Close()
}

func TestStdioTransport_SendJSONRPC_WritesSingleLine(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	go func() {
		line, err := proc.readStdin()
		if err != nil {
			return
		}
		if !strings.Contains(line, `"method":"test/send"`) {
			t.Errorf("unexpected stdin content: %s", line)
		}
		proc.closeStdout()
	}()

	body := []byte(`{"jsonrpc":"2.0","id":99,"method":"test/send","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.SendJSONRPC(ctx, body); err != nil {
		t.Fatalf("SendJSONRPC: %v", err)
	}
	_ = tr.Close()
}

func TestStdioTransport_RejectsConcurrentPromptStream(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Drain stdin so CallPromptStream's write doesn't block.
	go func() { _, _ = io.Copy(io.Discard, proc.stdinR) }()

	body := []byte(`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc1, err := tr.CallPromptStream(ctx, body)
	if err != nil {
		t.Fatalf("first CallPromptStream: %v", err)
	}
	t.Cleanup(func() { _ = rc1.Close() })

	_, err = tr.CallPromptStream(ctx, body)
	if err == nil {
		t.Fatal("expected error for concurrent prompt stream")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = tr.Close()
}

func TestStdioTransport_CloseUnblocksPendingUnary(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Drain stdin so CallUnary's write succeeds and it blocks waiting for a
	// response on the pending channel (the scenario we want to test).
	go func() { _, _ = io.Copy(io.Discard, proc.stdinR) }()

	// Start a unary call that never gets a response.
	body := []byte(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		data []byte
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		data, err := tr.CallUnary(ctx, body, 200)
		resCh <- result{data, err}
	}()

	// Wait for the goroutine to register the pending channel by polling for
	// the key's existence. This avoids a racy time.Sleep that could be flaky
	// on slow CI.
	waitPendingRegistered(t, tr, "7")

	// Close kills the process, closes stdout, causing readLoop to exit and
	// cleanup pending channels (which unblocks the waiting CallUnary).
	_ = tr.Close()

	select {
	case res := <-resCh:
		if res.err == nil {
			t.Fatal("expected error from closed transport during unary call")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CallUnary did not unblock after Close")
	}
}

func TestStdioTransport_CloseUnblocksPromptReader(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())

	// Drain stdin so CallPromptStream's write doesn't block.
	go func() { _, _ = io.Copy(io.Discard, proc.stdinR) }()

	body := []byte(`{"jsonrpc":"2.0","id":8,"method":"session/prompt","params":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rc, err := tr.CallPromptStream(ctx, body)
	if err != nil {
		t.Fatalf("CallPromptStream: %v", err)
	}

	// Close the transport — should unblock the prompt reader.
	_ = tr.Close()

	// Read should return EOF promptly.
	all, err := io.ReadAll(rc)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAll after Close: %v", err)
	}
	_ = all
}

func TestNewClientFromTransport_UsesProvidedTransport(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	t.Cleanup(killFunc(proc))
	tr := newStdioTransport(proc, slog.Default())
	c := newClientFromTransport(tr, slog.Default())
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.t != tr {
		t.Fatal("client transport mismatch")
	}
	_ = tr.Close()
}

// killFunc adapts Process.Kill for t.Cleanup which expects func().
func killFunc(p Process) func() {
	return func() { _ = p.Kill() }
}

// waitPendingRegistered polls the transport's pending map until key is present
// or the deadline expires. This replaces a racy time.Sleep with deterministic
// synchronization.
func waitPendingRegistered(t *testing.T, tr *stdioTransport, key string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tr.pendingMu.Lock()
		_, ok := tr.pending[key]
		tr.pendingMu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pending channel for key %q was not registered within 3s", key)
}
