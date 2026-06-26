package openaicodex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type codexStream struct {
	mu      sync.Mutex
	body    io.ReadCloser
	scanner *bufio.Scanner
	pending stream.PendingEventQueue
	mapper  *openairesponsestream.Mapper
	closed  bool
}

func newCodexStream(body io.ReadCloser, maxPending int) *codexStream {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	st := &codexStream{
		body:    body,
		scanner: sc,
		pending: stream.NewPendingEventQueue(maxPending),
	}
	st.mapper = openairesponsestream.New(&st.pending)
	return st
}

func (s *codexStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if ev, ok := s.pending.PopFront(); ok {
			s.mu.Unlock()
			return ev, nil
		}
		s.mu.Unlock()

		if !s.scanner.Scan() {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			if err := s.scanner.Err(); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, fmt.Errorf("%s: read stream: %w", ID, err)
			}
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(s.scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" || data == "[DONE]" {
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.handleData(data); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *codexStream) handleData(data string) error {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(data), &base); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	switch base.Type {
	case "response.created":
		return s.handleResponseCreated(data)
	case "response.output_text.delta":
		return s.handleOutputTextDelta(data)
	case "response.completed":
		return s.handleResponseCompleted(data)
	case "error":
		return s.handleStreamError(data)
	case "response.output_item.added":
		return s.handleOutputItemAdded(data)
	case "response.function_call_arguments.delta":
		return s.handleFunctionCallArgumentsDelta(data)
	case "response.function_call_arguments.done":
		return s.handleFunctionCallArgumentsDone(data)
	case "response.output_item.done":
		return s.handleOutputItemDone(data)
	default:
		return nil
	}
}

func (s *codexStream) handleOutputTextDelta(data string) error {
	var ev struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	return s.mapper.OutputTextDelta(ev.Delta)
}

func (s *codexStream) handleResponseCreated(data string) error {
	var ev struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	return s.mapper.ResponseCreated()
}

func (s *codexStream) handleResponseCompleted(data string) error {
	var ev struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	if err := s.mapper.BeginCompleted(); err != nil {
		return err
	}
	if len(ev.Response) > 0 {
		if !s.mapper.SawTextDelta() {
			if text := outputTextFromCompleted(ev.Response); text != "" {
				if err := s.mapper.CompletedTextFallback(text); err != nil {
					return err
				}
			}
		}
		for _, fc := range functionCallsFromCompleted(ev.Response) {
			if err := s.mapper.EmitCompletedToolCall(
				openairesponsestream.ToolCallID(fc.ID, fc.CallID),
				fc.Name,
				fc.Arguments,
			); err != nil {
				return err
			}
		}
		if usage := usageFromCompleted(ev.Response); usage != nil {
			if err := s.mapper.PushUsage(usage); err != nil {
				return err
			}
		}
	}
	return s.mapper.ResponseFinished()
}

func outputTextFromCompleted(raw json.RawMessage) string {
	var resp struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ""
	}
	var b strings.Builder
	for _, item := range resp.Output {
		for _, c := range item.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
			}
		}
	}
	return b.String()
}

type completedFunctionCall struct {
	ID        string
	CallID    string
	Name      string
	Arguments string
}

func functionCallsFromCompleted(raw json.RawMessage) []completedFunctionCall {
	var resp struct {
		Output []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	out := make([]completedFunctionCall, 0)
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		out = append(out, completedFunctionCall{
			ID:        item.ID,
			CallID:    item.CallID,
			Name:      item.Name,
			Arguments: item.Arguments,
		})
	}
	return out
}

func (s *codexStream) handleOutputItemDone(data string) error {
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
	return s.mapper.FinishToolCallArguments(
		openairesponsestream.ToolCallID(ev.Item.ID, ev.Item.CallID),
		ev.Item.Name,
		ev.Item.Arguments,
	)
}

func (s *codexStream) handleStreamError(data string) error {
	var ev struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	msg := ""
	if ev.Error != nil {
		msg = ev.Error.Message
	}
	return s.mapper.StreamError("", msg, "upstream error")
}

func (s *codexStream) handleOutputItemAdded(data string) error {
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
	return s.mapper.ToolCallAdded(openairesponsestream.ToolCallID(ev.Item.ID, ev.Item.CallID), ev.Item.Name)
}

func (s *codexStream) handleFunctionCallArgumentsDelta(data string) error {
	var ev struct {
		ItemID string `json:"item_id"`
		CallID string `json:"call_id"`
		Delta  string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	return s.mapper.ToolCallArgsDelta(openairesponsestream.ToolCallID(ev.ItemID, ev.CallID), ev.Delta)
}

func (s *codexStream) handleFunctionCallArgumentsDone(data string) error {
	var ev struct {
		ItemID    string `json:"item_id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return fmt.Errorf("%s: malformed stream event: %w", ID, err)
	}
	return s.mapper.FinishToolCallArguments(openairesponsestream.ToolCallID(ev.ItemID, ev.CallID), ev.Name, ev.Arguments)
}

func usageFromCompleted(raw json.RawMessage) *lipapi.Event {
	var resp struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	u := resp.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens: safecast.IntFromInt64Clamp(u.OutputTokens),
		TotalTokens:  safecast.IntFromInt64Clamp(u.TotalTokens),
	}
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
