package fakebridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
)

type holdRun struct {
	terminalEmitted bool
}

// Harness is an in-process fake bridge over io.Reader/Writer pairs.
type Harness struct {
	script Script

	mu             sync.Mutex
	blockCancel    bool
	ignoreShutdown bool
	closed         bool
	exitCode       *int
	stderr         bytes.Buffer
	agentSeq       atomic.Int64
	runSeq         atomic.Int64
	sendSeq        atomic.Int64
	lastRunID      string
	holdRuns       map[string]*holdRun
	canceledRuns   map[string]struct{}
	outMu          sync.Mutex
}

// New returns a harness with the given script.
func New(script Script) *Harness {
	if script.ImplVersion == "" {
		script.ImplVersion = "fake-1.0.0"
	}
	if script.SDKVersion == "" {
		script.SDKVersion = protocol.PinnedSDKVersion
	}
	if script.Generation == 0 {
		script.Generation = 1
	}
	if script.OnMethod == nil {
		script.OnMethod = map[string][]Action{}
	}
	return &Harness{
		script:       script,
		holdRuns:     map[string]*holdRun{},
		canceledRuns: map[string]struct{}{},
	}
}

// StderrText returns accumulated stderr diagnostics.
func (h *Harness) StderrText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stderr.String()
}

// ExitCode returns the scripted exit code when the harness requested exit.
func (h *Harness) ExitCode() (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exitCode == nil {
		return 0, false
	}
	return *h.exitCode, true
}

// Run processes NDJSON requests from in and writes responses/events to out
// until EOF, shutdown, or a scripted exit.
//
// run/cancel is handled on the reader goroutine so it can interrupt a blocking
// wait_for_file on the worker path without waiting for the gate file.
func (h *Harness) Run(in io.Reader, out io.Writer) error {
	for _, action := range h.script.OnStartup {
		if err := h.applyAction(out, nil, action); err != nil {
			return err
		}
		if h.shouldStop() {
			return nil
		}
	}

	type inbound struct {
		req *protocol.Frame
		err error
	}
	ch := make(chan inbound, 8)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64*1024), protocol.MaxFrameBytes+1)
		for sc.Scan() {
			line := sc.Bytes()
			req, err := protocol.DecodeLine(line)
			if err != nil {
				h.writeStderr(fmt.Sprintf("decode error: %v", err))
				continue
			}
			// Always handle run/cancel on the reader path so a blocking
			// wait_for_file worker can observe markRunCanceled. Callers that
			// buffer send+cancel together must wait for the send response
			// before writing cancel (see harness_test).
			if req.Method == protocol.MethodRunCancel {
				if err := h.handleRequest(out, req); err != nil {
					ch <- inbound{err: err}
					return
				}
				continue
			}
			ch <- inbound{req: req}
		}
		if err := sc.Err(); err != nil {
			ch <- inbound{err: err}
		}
	}()

	for item := range ch {
		if item.err != nil {
			return item.err
		}
		if item.req == nil {
			continue
		}
		if h.shouldStop() {
			return nil
		}
		if err := h.handleRequest(out, item.req); err != nil {
			return err
		}
		if h.shouldStop() {
			return nil
		}
	}
	return nil
}

func (h *Harness) markRunCanceled(runID string) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.canceledRuns == nil {
		h.canceledRuns = map[string]struct{}{}
	}
	h.canceledRuns[runID] = struct{}{}
}

func (h *Harness) isRunCanceled(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.canceledRuns[runID]
	return ok
}

func (h *Harness) handleRequest(out io.Writer, req *protocol.Frame) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	blockCancel := h.blockCancel
	ignoreShutdown := h.ignoreShutdown
	h.mu.Unlock()

	switch req.Method {
	case protocol.MethodInitialize:
		result := map[string]any{
			"schemaVersion": protocol.SchemaVersion,
			"implVersion":   h.script.ImplVersion,
			"sdkVersion":    h.script.SDKVersion,
			"nodeVersion":   "22.13.0",
			"capabilities":  protocol.RequiredMethods(),
		}
		if !h.script.OmitSandboxSupported {
			sandboxSupported := true
			if h.script.SandboxSupported != nil {
				sandboxSupported = *h.script.SandboxSupported
			}
			result["sandboxSupported"] = sandboxSupported
		}
		return h.writeResponse(out, req, result)
	case protocol.MethodHealth:
		result := map[string]any{
			"ok":         true,
			"generation": h.script.Generation,
		}
		if !h.script.OmitSandboxSupported {
			sandboxSupported := true
			if h.script.SandboxSupported != nil {
				sandboxSupported = *h.script.SandboxSupported
			}
			result["sandboxSupported"] = sandboxSupported
		}
		return h.writeResponse(out, req, result)
	case protocol.MethodModelsList:
		models := h.script.Models
		if len(models) == 0 {
			models = json.RawMessage("[]")
		}
		raw, err := json.Marshal(map[string]json.RawMessage{"models": models})
		if err != nil {
			return err
		}
		return h.writeResponseRaw(out, req, raw)
	case protocol.MethodAgentCreate:
		n := h.agentSeq.Add(1)
		agentID := fmt.Sprintf("agent-%d", n)
		if path := strings.TrimSpace(h.script.CreateCountFile); path != "" {
			// Accumulate across process restarts so create-count proofs survive kill/reap.
			next := int64(1)
			if raw, err := os.ReadFile(path); err == nil {
				if prev, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && prev > 0 {
					next = prev + 1
				}
			}
			if err := os.WriteFile(path, []byte(strconv.FormatInt(next, 10)+"\n"), 0o644); err != nil {
				return err
			}
		}
		return h.writeResponse(out, req, map[string]any{"agentId": agentID})
	case protocol.MethodAgentSend:
		// Each send starts clean so a prior run/cancel cannot suppress events on
		// a later send that reuses a fixed scripted runId.
		h.mu.Lock()
		h.canceledRuns = map[string]struct{}{}
		h.mu.Unlock()
		if path := strings.TrimSpace(h.script.PromptCaptureFile); path != "" {
			var params protocol.AgentSendParams
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params, &params)
			}
			_ = os.WriteFile(path, []byte(params.Prompt), 0o644)
		}
		n := int(h.sendSeq.Add(1))
		if n <= len(h.script.OnAgentSend) && len(h.script.OnAgentSend[n-1]) > 0 {
			return h.runActions(out, req, h.script.OnAgentSend[n-1])
		}
		if actions := h.script.OnMethod[protocol.MethodAgentSend]; len(actions) > 0 {
			return h.runMethodActions(out, req, protocol.MethodAgentSend, nil)
		}
		runID := h.allocateRunID()
		if err := h.writeResponse(out, req, map[string]any{"runId": runID}); err != nil {
			return err
		}
		seq1, seq2 := int64(1), int64(2)
		if err := h.writeEvent(out, &protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeEvent,
			RunID:         runID,
			Seq:           &seq1,
			Kind:          protocol.KindTextDelta,
			Payload:       json.RawMessage(`{"text":"hello"}`),
		}); err != nil {
			return err
		}
		return h.writeEvent(out, &protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeEvent,
			RunID:         runID,
			Seq:           &seq2,
			Kind:          protocol.KindFinished,
			Payload:       json.RawMessage(`{"status":"finished"}`),
		})
	case protocol.MethodRunCancel:
		if blockCancel {
			h.writeStderr("cancel blocked")
			return nil
		}
		var params protocol.RunCancelParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		h.markRunCanceled(params.RunID)
		if err := h.writeResponse(out, req, map[string]any{"cancelled": true}); err != nil {
			return err
		}
		return h.emitCancelledTerminal(out, params.RunID)
	case protocol.MethodAgentDispose:
		return h.writeResponse(out, req, map[string]any{"disposed": true})
	case protocol.MethodBridgeShutdown:
		if ignoreShutdown {
			h.writeStderr("shutdown ignored")
			return nil
		}
		if err := h.writeResponse(out, req, map[string]any{"shutdown": true}); err != nil {
			return err
		}
		h.mu.Lock()
		h.closed = true
		h.mu.Unlock()
		return nil
	default:
		return h.runMethodActions(out, req, req.Method, nil)
	}
}

func (h *Harness) runMethodActions(out io.Writer, req *protocol.Frame, method string, fallback []Action) error {
	actions := h.script.OnMethod[method]
	if len(actions) == 0 {
		actions = fallback
	}
	if len(actions) == 0 {
		return h.writeResponse(out, req, map[string]any{"ok": true})
	}
	return h.runActions(out, req, actions)
}

func (h *Harness) runActions(out io.Writer, req *protocol.Frame, actions []Action) error {
	for _, action := range actions {
		if action.Type != ActionRespond {
			runID, _ := h.resolveRunID(action.RunID, true)
			if h.isRunCanceled(runID) {
				return nil
			}
		}
		if err := h.applyAction(out, req, action); err != nil {
			return err
		}
	}
	return nil
}

func (h *Harness) applyAction(out io.Writer, req *protocol.Frame, action Action) error {
	switch action.Type {
	case ActionRespond:
		if len(action.Result) > 0 {
			raw, err := h.materializeRespondResult(action.Result)
			if err != nil {
				return err
			}
			return h.writeResponseRaw(out, req, raw)
		}
		if len(action.Error) > 0 {
			var body protocol.ErrorBody
			if err := json.Unmarshal(action.Error, &body); err != nil {
				return err
			}
			return h.writeEvent(out, &protocol.Frame{
				SchemaVersion: protocol.SchemaVersion,
				Type:          protocol.TypeResponse,
				ID:            req.ID,
				Method:        req.Method,
				Error:         &body,
			})
		}
		return h.writeResponse(out, req, map[string]any{"ok": true})
	case ActionEvent:
		seq := action.Seq
		runID, err := h.resolveRunID(action.RunID, true)
		if err != nil {
			return err
		}
		return h.writeEvent(out, &protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeEvent,
			RunID:         runID,
			Seq:           &seq,
			Kind:          action.Kind,
			Payload:       action.Payload,
		})
	case ActionOutOfOrderEvents:
		seq2 := int64(2)
		seq1 := int64(1)
		if err := h.writeEvent(out, &protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeEvent,
			RunID:         "run-1",
			Seq:           &seq2,
			Kind:          protocol.KindTextDelta,
			Payload:       json.RawMessage(`{"text":"second"}`),
		}); err != nil {
			return err
		}
		return h.writeEvent(out, &protocol.Frame{
			SchemaVersion: protocol.SchemaVersion,
			Type:          protocol.TypeEvent,
			RunID:         "run-1",
			Seq:           &seq1,
			Kind:          protocol.KindTextDelta,
			Payload:       json.RawMessage(`{"text":"first"}`),
		})
	case ActionStderr:
		h.writeStderr(action.Text)
		return nil
	case ActionMalformed:
		line := action.Line
		if line == "" {
			line = "{not-json"
		}
		h.outMu.Lock()
		_, err := io.WriteString(out, line+"\n")
		h.outMu.Unlock()
		return err
	case ActionOversized:
		n := action.Bytes
		if n <= 0 {
			n = protocol.MaxFrameBytes + 1
		}
		prefix := `{"schemaVersion":1,"type":"event","runId":"run-1","seq":1,"kind":"text_delta","payload":{"text":"`
		suffix := `"}}`
		padLen := n - len(prefix) - len(suffix)
		if padLen < 1 {
			padLen = 1
			n = len(prefix) + padLen + len(suffix)
		}
		pad := strings.Repeat("x", padLen)
		line := prefix + pad + suffix
		if len(line) < n {
			line += strings.Repeat("x", n-len(line))
		}
		if len(line) > n {
			line = line[:n]
		}
		h.outMu.Lock()
		_, err := io.WriteString(out, line+"\n")
		h.outMu.Unlock()
		return err
	case ActionExit:
		code := action.Code
		h.mu.Lock()
		h.exitCode = &code
		h.closed = true
		h.mu.Unlock()
		return nil
	case ActionBlockCancel:
		h.mu.Lock()
		h.blockCancel = true
		h.mu.Unlock()
		return nil
	case ActionIgnoreShutdown:
		h.mu.Lock()
		h.ignoreShutdown = true
		h.mu.Unlock()
		return nil
	case ActionSleep:
		ms := action.Ms
		if ms <= 0 {
			ms = 50
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return nil
	case ActionWaitForFile:
		path := strings.TrimSpace(action.Path)
		if path == "" {
			return fmt.Errorf("fakebridge: wait_for_file missing path")
		}
		timeout := time.Duration(action.Ms) * time.Millisecond
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		deadline := time.Now().Add(timeout)
		runID, _ := h.resolveRunID(action.RunID, true)
		for {
			if h.shouldStop() {
				return nil
			}
			if runID != "" && h.isRunCanceled(runID) {
				return nil
			}
			if _, err := os.Stat(path); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("fakebridge: wait_for_file timeout path=%s", path)
			}
			time.Sleep(10 * time.Millisecond)
		}
	case ActionHoldUntilCancel:
		runID, err := h.resolveRunID(action.RunID, true)
		if err != nil {
			return err
		}
		h.mu.Lock()
		if h.holdRuns == nil {
			h.holdRuns = map[string]*holdRun{}
		}
		if _, ok := h.holdRuns[runID]; !ok {
			h.holdRuns[runID] = &holdRun{}
		}
		h.mu.Unlock()
		if path := strings.TrimSpace(action.Path); path != "" {
			if err := os.WriteFile(path, []byte("active\n"), 0o644); err != nil {
				return fmt.Errorf("fakebridge: hold notify file: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("fakebridge: unknown action %q", action.Type)
	}
}

func (h *Harness) allocateRunID() string {
	runID := fmt.Sprintf("run-%d", h.runSeq.Add(1))
	h.mu.Lock()
	h.lastRunID = runID
	h.mu.Unlock()
	return runID
}

func (h *Harness) resolveRunID(in string, allowLast bool) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" || in == AutoRunID {
		if !allowLast && in == "" {
			return "", fmt.Errorf("fakebridge: missing runId")
		}
		h.mu.Lock()
		last := h.lastRunID
		h.mu.Unlock()
		if last == "" {
			return "", fmt.Errorf("fakebridge: no prior runId for %q", AutoRunID)
		}
		return last, nil
	}
	return in, nil
}

func (h *Harness) materializeRespondResult(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, nil
	}
	rid, _ := m["runId"].(string)
	rid = strings.TrimSpace(rid)
	if rid == AutoRunID || rid == "" {
		if _, has := m["runId"]; has || rid == AutoRunID {
			m["runId"] = h.allocateRunID()
			out, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			return out, nil
		}
	}
	if rid != "" {
		h.mu.Lock()
		h.lastRunID = rid
		h.mu.Unlock()
	}
	return raw, nil
}

func (h *Harness) emitCancelledTerminal(out io.Writer, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	h.mu.Lock()
	if h.holdRuns == nil {
		h.holdRuns = map[string]*holdRun{}
	}
	run := h.holdRuns[runID]
	if run == nil {
		// wait_for_file (and other non-hold) cancels still need one terminal so
		// RunStream.Cancel can complete without timing out the cleanup budget.
		run = &holdRun{}
		h.holdRuns[runID] = run
	}
	if run.terminalEmitted {
		h.mu.Unlock()
		return nil
	}
	run.terminalEmitted = true
	h.mu.Unlock()
	seq := int64(1)
	return h.writeEvent(out, &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeEvent,
		RunID:         runID,
		Seq:           &seq,
		Kind:          protocol.KindFinished,
		Payload:       json.RawMessage(`{"status":"cancelled"}`),
	})
}

func (h *Harness) writeResponse(out io.Writer, req *protocol.Frame, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return h.writeResponseRaw(out, req, raw)
}

func (h *Harness) writeResponseRaw(out io.Writer, req *protocol.Frame, result json.RawMessage) error {
	return h.writeEvent(out, &protocol.Frame{
		SchemaVersion: protocol.SchemaVersion,
		Type:          protocol.TypeResponse,
		ID:            req.ID,
		Method:        req.Method,
		Result:        result,
	})
}

func (h *Harness) writeEvent(out io.Writer, frame *protocol.Frame) error {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	return protocol.WriteFrame(out, frame)
}

func (h *Harness) writeStderr(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stderr.Len() > 0 {
		h.stderr.WriteByte('\n')
	}
	h.stderr.WriteString(text)
}

func (h *Harness) shouldStop() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}
