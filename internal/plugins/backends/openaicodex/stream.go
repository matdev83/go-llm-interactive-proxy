package openaicodex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// codexEventMapper holds the canonical-event mapping state shared by SSE and
// WebSocket transports. It is not concurrency-safe; callers must serialize
// handleData calls (the EventPump does this under its lock).
type codexEventMapper struct {
	pending     stream.PendingEventQueue
	mapper      *openairesponsestream.Mapper
	responseID  string
	outputItems []inputItem
	toolCallIDs map[string]string
	provisional map[string]bool
	terminal    bool
}

func newCodexEventMapper(maxPending int) *codexEventMapper {
	m := &codexEventMapper{
		pending:     stream.NewPendingEventQueue(maxPending),
		toolCallIDs: make(map[string]string),
		provisional: make(map[string]bool),
	}
	m.mapper = openairesponsestream.New(&m.pending)
	return m
}

func (m *codexEventMapper) handleData(data string) error {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(data), &base); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	switch base.Type {
	case "response.created":
		return m.handleResponseCreated(data)
	case "response.output_text.delta":
		return m.handleOutputTextDelta(data)
	case "response.completed":
		return m.handleResponseCompleted(data)
	case "error":
		return m.handleStreamError(data)
	case "response.output_item.added":
		return m.handleOutputItemAdded(data)
	case "response.function_call_arguments.delta":
		return m.handleFunctionCallArgumentsDelta(data)
	case "response.function_call_arguments.done":
		return m.handleFunctionCallArgumentsDone(data)
	case "response.output_item.done":
		return m.handleOutputItemDone(data)
	default:
		return nil
	}
}

func (m *codexEventMapper) handleOutputTextDelta(data string) error {
	var ev struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if looksLikeToolProtocolText(ev.Delta) {
		return m.mapper.StreamError("tool_protocol_text_leak", "upstream emitted tool-call protocol as text", "upstream emitted tool-call protocol as text")
	}
	return m.mapper.OutputTextDelta(ev.Delta)
}

func looksLikeToolProtocolText(delta string) bool {
	text := strings.TrimSpace(delta)
	if text == "" {
		return false
	}
	// Treat suspected textual tool-call protocol as a stream error instead of
	// dropping it silently: leaking tool syntax to the client is more damaging
	// than the small false-positive risk for ordinary assistant prose.
	if strings.Contains(text, "to=functions.") || strings.Contains(text, "to=functions_") {
		return true
	}
	if strings.HasPrefix(text, "{") && (strings.Contains(text, `"filePath"`) || strings.Contains(text, `"offset"`) || strings.Contains(text, `"limit"`)) {
		return true
	}
	return false
}

func (m *codexEventMapper) handleResponseCreated(data string) error {
	var ev struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	m.responseID = strings.TrimSpace(ev.Response.ID)
	return m.mapper.ResponseCreated()
}

func (m *codexEventMapper) handleResponseCompleted(data string) error {
	var ev struct {
		Response completedResponse `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if err := m.mapper.BeginCompleted(); err != nil {
		return err
	}
	if id := strings.TrimSpace(ev.Response.ID); id != "" {
		m.responseID = id
	}
	if !m.mapper.SawTextDelta() {
		if text := ev.Response.outputText(); text != "" {
			if err := m.mapper.CompletedTextFallback(text); err != nil {
				return err
			}
		}
	}
	for _, item := range ev.Response.Output {
		if item.Type != "function_call" {
			continue
		}
		if err := m.mapper.EmitCompletedToolCall(
			codexCanonicalToolCallID(item.ID, item.CallID),
			item.Name,
			item.Arguments,
		); err != nil {
			return err
		}
	}
	if usage := ev.Response.usageEvent(); usage != nil {
		if err := m.mapper.PushUsage(usage); err != nil {
			return err
		}
	}
	if err := m.mapper.ResponseFinished(); err != nil {
		return err
	}
	m.terminal = true
	return nil
}

type completedResponse struct {
	ID     string `json:"id"`
	Output []struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (r completedResponse) outputText() string {
	var b strings.Builder
	for _, item := range r.Output {
		for _, c := range item.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}

func (r completedResponse) usageEvent() *lipapi.Event {
	u := r.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens: safecast.IntFromInt64Clamp(u.OutputTokens),
		TotalTokens:  safecast.IntFromInt64Clamp(u.TotalTokens),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
}

func (m *codexEventMapper) handleOutputItemDone(data string) error {
	var ev struct {
		Item struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if ev.Item.Type != "function_call" {
		return nil
	}
	m.rememberToolCallID(ev.Item.ID, ev.Item.CallID)
	m.remapProvisionalToolCall(ev.Item.ID, ev.Item.CallID)
	if item, ok := outputFunctionCallInputItem(ev.Item.Type, ev.Item.ID, ev.Item.CallID, ev.Item.Name, ev.Item.Arguments); ok {
		m.outputItems = append(m.outputItems, item)
	}
	return m.mapper.FinishToolCallArguments(
		codexCanonicalToolCallID(ev.Item.ID, ev.Item.CallID),
		ev.Item.Name,
		ev.Item.Arguments,
	)
}

func outputFunctionCallInputItem(itemType, id, callID, name, arguments string) (functionCallItem, bool) {
	if itemType != "function_call" {
		return functionCallItem{}, false
	}
	hadCallID := strings.TrimSpace(callID) != ""
	callID = strings.TrimSpace(callID)
	id = strings.TrimSpace(id)
	if callID == "" {
		callID = id
	}
	name = strings.TrimSpace(name)
	if callID == "" || name == "" {
		return functionCallItem{}, false
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	item := functionCallItem{
		Type:      "function_call",
		CallID:    callID,
		Name:      name,
		Arguments: arguments,
	}
	if id != "" && hadCallID {
		item.ID = id
	}
	return item, true
}

func (m *codexEventMapper) handleStreamError(data string) error {
	var ev struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	msg := ""
	code := ""
	if ev.Error != nil {
		code = ev.Error.Code
		msg = ev.Error.Message
	}
	if debugTurnsEnabled() {
		slog.Debug("openaicodex.debug.upstream_error", "code", code, "message", msg)
	}
	return m.mapper.StreamError(code, msg, "upstream error")
}

func (m *codexEventMapper) handleOutputItemAdded(data string) error {
	var ev struct {
		Item struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if ev.Item.Type != "function_call" {
		return nil
	}
	m.rememberToolCallID(ev.Item.ID, ev.Item.CallID)
	m.remapProvisionalToolCall(ev.Item.ID, ev.Item.CallID)
	return m.mapper.ToolCallAdded(codexCanonicalToolCallID(ev.Item.ID, ev.Item.CallID), ev.Item.Name)
}

func (m *codexEventMapper) handleFunctionCallArgumentsDelta(data string) error {
	var ev struct {
		ItemID string `json:"item_id"`
		CallID string `json:"call_id"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if codexToolDeltaDebugEnabled() {
		slog.Debug("openaicodex.tool_args_delta", "item_id", ev.ItemID, "call_id", ev.CallID, "delta", truncateDebug(ev.Delta, 512))
	}
	return m.mapper.ToolCallArgsDelta(m.toolCallID(ev.ItemID, ev.CallID), ev.Delta)
}

func (m *codexEventMapper) handleFunctionCallArgumentsDone(data string) error {
	var ev struct {
		ItemID    string `json:"item_id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if codexToolDebugEnabled() {
		slog.Debug("openaicodex.tool_args_done", "item_id", ev.ItemID, "call_id", ev.CallID, "name", ev.Name, "arguments", truncateDebug(ev.Arguments, 512))
	}
	return m.mapper.FinishToolCallArguments(m.toolCallID(ev.ItemID, ev.CallID), ev.Name, ev.Arguments)
}

func codexCanonicalToolCallID(itemID, callID string) string {
	return openairesponsestream.ToolCallID(callID, itemID)
}

func (m *codexEventMapper) rememberToolCallID(itemID, callID string) {
	itemID = strings.TrimSpace(itemID)
	callID = strings.TrimSpace(callID)
	if itemID == "" || callID == "" {
		return
	}
	m.toolCallIDs[itemID] = callID
	// Once the real call_id is known, drop the provisional flag so toolCallID
	// stops returning the item-only ID and all subsequent events canonicalize
	// onto the call_id.
	delete(m.provisional, itemID)
}

// remapProvisionalToolCall moves any mapper state buffered under the
// provisional item-only ID onto the real call_id once it is learned. Without
// this, argument deltas that arrived before output_item.added stay buffered
// under the item ID while ToolCallAdded targets the call_id, fragmenting one
// logical tool call into two.
func (m *codexEventMapper) remapProvisionalToolCall(itemID, callID string) {
	itemID = strings.TrimSpace(itemID)
	callID = strings.TrimSpace(callID)
	if itemID == "" || callID == "" || callID == itemID {
		return
	}
	m.mapper.RemapToolCallID(itemID, callID)
}

func (m *codexEventMapper) toolCallID(itemID, callID string) string {
	itemID = strings.TrimSpace(itemID)
	callID = strings.TrimSpace(callID)
	// Prefer a learned call_id over the provisional item-only ID so deltas and
	// completion events resolve to the same canonical ID as output_item.added.
	if callID == "" {
		callID = strings.TrimSpace(m.toolCallIDs[itemID])
	}
	if callID != "" {
		return codexCanonicalToolCallID(itemID, callID)
	}
	if itemID != "" && m.provisional[itemID] {
		return itemID
	}
	if callID == "" && itemID != "" {
		m.provisional[itemID] = true
	}
	return codexCanonicalToolCallID(itemID, callID)
}

func truncateDebug(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var _ lipapi.ManagedEventStream = (*codexStream)(nil)

type codexStream struct {
	mapper  *codexEventMapper
	mu      sync.Mutex
	body    io.ReadCloser
	scanner *bufio.Scanner
	closed  bool
}

func newCodexStream(body io.ReadCloser, maxPending int) *codexStream {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	st := &codexStream{
		mapper:  newCodexEventMapper(maxPending),
		body:    body,
		scanner: sc,
	}
	return st
}

func (s *codexStream) Recv(ctx context.Context) (lipapi.Event, error) {
	pump := stream.EventPump[string]{
		Lock:     &s.mu,
		Pending:  &s.mapper.pending,
		IsClosed: func() bool { return s.closed },
		Read:     s.readData,
		Handle:   s.mapper.handleData,
	}
	return pump.Recv(ctx)
}

func (s *codexStream) readData() (string, bool, error) {
	for s.scanner.Scan() {
		line := strings.TrimSpace(s.scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" || data == "[DONE]" {
			continue
		}
		return data, true, nil
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("%s: read stream: %w", ID, err)
	}
	return "", false, nil
}

func (s *codexStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.body == nil {
		return nil
	}
	err := s.body.Close()
	s.body = nil
	return err
}

func (s *codexStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}
