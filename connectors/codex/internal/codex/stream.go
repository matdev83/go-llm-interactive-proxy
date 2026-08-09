package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/reasoning"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/responseitem"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/responsestream"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/codex/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/streampump"
)

// codexEventMapper holds the canonical-event mapping state shared by SSE and
// WebSocket transports. It is not concurrency-safe; callers must serialize
// handleData calls (the EventPump does this under its lock).
type codexEventMapper struct {
	pending                   streampump.PendingEventQueue
	mapper                    *responsestream.Mapper
	reasoningSummarySanitizer reasoning.SummarySanitizer
	responseID                string
	outputItems               []inputItem
	outputItemIndexes         map[string]int
	outputItemPayloads        map[string]string
	toolCallIDs               map[string]string
	provisional               map[string]bool
	terminal                  bool
}

func newCodexEventMapper(maxPending int) *codexEventMapper {
	m := &codexEventMapper{
		pending:            streampump.NewPendingEventQueue(maxPending),
		toolCallIDs:        make(map[string]string),
		provisional:        make(map[string]bool),
		outputItemIndexes:  make(map[string]int),
		outputItemPayloads: make(map[string]string),
	}
	m.mapper = responsestream.New(&m.pending)
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
	case "response.reasoning_summary_part.added":
		m.reasoningSummarySanitizer.StartSummaryPart()
		return nil
	case "response.reasoning_summary_text.delta":
		return m.handleReasoningDelta(data, true)
	case "response.reasoning_text.delta":
		return m.handleReasoningDelta(data, false)
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

func (m *codexEventMapper) handleReasoningDelta(data string, stripEmptyHTMLComments bool) error {
	var ev struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if stripEmptyHTMLComments {
		ev.Delta = m.reasoningSummarySanitizer.SanitizeDelta(ev.Delta)
		if ev.Delta == "" {
			return nil
		}
	}
	// Codex requests encrypted reasoning and emits the complete item at
	// output_item.done. Keep deltas mapper-private so preservation captures one
	// exact Responses item rather than an additional lossy chat-text artifact.
	return nil
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
	m.reasoningSummarySanitizer.Reset()
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
		if err := m.handleCompletedOutputItem(item); err != nil {
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
	m.reasoningSummarySanitizer.Reset()
	m.terminal = true
	return nil
}

type completedResponse struct {
	ID     string            `json:"id"`
	Output []json.RawMessage `json:"output"`
	Usage  *completedUsage   `json:"usage"`
}

type completedUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
}

func (r completedResponse) outputText() string {
	var b strings.Builder
	for _, raw := range r.Output {
		var item struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
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
	if u == nil {
		return nil
	}
	presence := lipapi.UsagePresence{
		InputTokens:  u.InputTokens != nil,
		OutputTokens: u.OutputTokens != nil,
		TotalTokens:  u.TotalTokens != nil,
	}
	if !presence.Any() {
		return nil
	}
	return &lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   completedUsageValue(u.InputTokens),
		OutputTokens:  completedUsageValue(u.OutputTokens),
		TotalTokens:   completedUsageValue(u.TotalTokens),
		UsagePresence: presence,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
}

func completedUsageValue(value *int64) int {
	if value == nil {
		return 0
	}
	return safecast.IntFromInt64Clamp(*value)
}

func (m *codexEventMapper) handleOutputItemDone(data string) error {
	var ev struct {
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	return m.handleCompletedOutputItem(ev.Item)
}

func (m *codexEventMapper) handleCompletedOutputItem(raw json.RawMessage) error {
	var item struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return fmt.Errorf("%s: malformed output item: %w", ID, err)
	}
	key := item.Type + "\x00" + item.ID
	if item.ID == "" {
		key = item.Type + "\x00" + string(raw)
	}
	switch item.Type {
	case "function_call":
		if _, seen := m.outputItemIndexes[key]; seen {
			return nil
		}
		m.rememberToolCallID(item.ID, item.CallID)
		m.remapProvisionalToolCall(item.ID, item.CallID)
		output, ok := outputFunctionCallInputItem(item.Type, item.ID, item.CallID, item.Name, item.Arguments)
		if !ok {
			return nil
		}
		m.outputItemIndexes[key] = len(m.outputItems)
		m.outputItems = append(m.outputItems, output)
		return m.mapper.FinishToolCallArguments(codexCanonicalToolCallID(item.ID, item.CallID), item.Name, item.Arguments)
	case "reasoning":
		opaque, err := responseitem.CanonizeReasoningItemOpaque(raw)
		if err != nil {
			return fmt.Errorf("%s: invalid reasoning output item: %w", ID, err)
		}
		payload := string(opaque)
		if previous, seen := m.outputItemPayloads[key]; seen && previous == payload {
			return nil
		}
		retained := opaqueResponseItem{raw: append(json.RawMessage(nil), opaque...)}
		if index, seen := m.outputItemIndexes[key]; seen {
			m.outputItems[index] = retained
		} else {
			m.outputItemIndexes[key] = len(m.outputItems)
			m.outputItems = append(m.outputItems, retained)
		}
		m.outputItemPayloads[key] = payload
		return m.mapper.ReasoningPart(&lipapi.ReasoningPart{
			Dialect: lipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		})
	case "compaction":
		payload := string(raw)
		if previous, seen := m.outputItemPayloads[key]; seen && previous == payload {
			return nil
		}
		retained := opaqueResponseItem{raw: append(json.RawMessage(nil), raw...)}
		if index, seen := m.outputItemIndexes[key]; seen {
			m.outputItems[index] = retained
		} else {
			m.outputItemIndexes[key] = len(m.outputItems)
			m.outputItems = append(m.outputItems, retained)
		}
		m.outputItemPayloads[key] = payload
	}
	return nil
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
		slog.Debug("codex.debug.upstream_error", "code", code, "message_bytes", len(msg))
	}
	m.reasoningSummarySanitizer.Reset()
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
		slog.Debug("codex.tool_args_delta", "item_id_present", ev.ItemID != "", "call_id_present", ev.CallID != "", "delta_bytes", len(ev.Delta))
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
		slog.Debug("codex.tool_args_done", "item_id_present", ev.ItemID != "", "call_id_present", ev.CallID != "", "name_bytes", len(ev.Name), "arguments_bytes", len(ev.Arguments))
	}
	return m.mapper.FinishToolCallArguments(m.toolCallID(ev.ItemID, ev.CallID), ev.Name, ev.Arguments)
}

func codexCanonicalToolCallID(itemID, callID string) string {
	return responsestream.ToolCallID(callID, itemID)
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
	pump := streampump.EventPump[string]{
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
		data, ok := sseDataLine(s.scanner.Text())
		if ok {
			return data, true, nil
		}
	}
	if err := s.scanner.Err(); err != nil {
		return "", false, fmt.Errorf("%s: read stream: %w", ID, err)
	}
	return "", false, nil
}

func sseDataLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return "", false
	}
	return data, true
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
