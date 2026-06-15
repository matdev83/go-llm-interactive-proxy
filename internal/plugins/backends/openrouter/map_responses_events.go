package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type responsesStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *ssestream.Stream[responses.ResponseStreamEventUnion]

	pending      stream.PendingEventQueue
	sawResp      bool
	sawMsg       bool
	sawTextDelta bool
	closed       bool

	toolCallStarted   map[string]bool
	toolCallArgDeltas map[string]bool
	toolCallFinished  map[string]bool
}

func newResponsesStream(s *ssestream.Stream[responses.ResponseStreamEventUnion], maxPending int) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &responsesStream{
		sdk:               s,
		pending:           stream.NewPendingEventQueue(maxPending),
		toolCallStarted:   map[string]bool{},
		toolCallArgDeltas: map[string]bool{},
		toolCallFinished:  map[string]bool{},
	}
}

func (s *responsesStream) Recv(ctx context.Context) (lipapi.Event, error) {
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

		if !s.sdk.Next() {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			if err := s.sdk.Err(); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, fmt.Errorf("openrouter: recv responses stream: %w", err)
			}
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		cur := s.sdk.Current()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.handleUnion(cur); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *responsesStream) handleUnion(cur responses.ResponseStreamEventUnion) error {
	switch cur.Type {
	case "response.created":
		if !s.sawResp {
			s.sawResp = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
	case "response.output_text.delta":
		if !s.sawResp {
			s.sawResp = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMsg {
			s.sawMsg = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		if cur.Delta != "" {
			s.sawTextDelta = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: cur.Delta}); err != nil {
				return err
			}
		}
	case "response.completed":
		resp := cur.Response
		if !s.sawResp {
			s.sawResp = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMsg {
			s.sawMsg = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		if !s.sawTextDelta {
			text := resp.OutputText()
			if text != "" {
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text}); err != nil {
					return err
				}
			}
		}
		if err := s.emitOutputMedia(resp); err != nil {
			return err
		}
		if err := s.emitToolCallsFromCompletedResponse(resp); err != nil {
			return err
		}
		if usage := s.usageFromResponse(resp); usage != nil {
			if err := s.pending.Push(*usage); err != nil {
				return err
			}
		}
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
			return err
		}
	case "error":
		ev := cur.AsError()
		if !s.sawResp {
			s.sawResp = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		msg := ev.Message
		if msg == "" {
			msg = "stream error"
		}
		if err := s.pending.Push(lipapi.Event{
			Kind:         lipapi.EventError,
			ErrorCode:    ev.Code,
			ErrorMessage: msg,
		}); err != nil {
			return err
		}
	case "response.output_item.added":
		addEv := cur.AsResponseOutputItemAdded()
		item := addEv.Item
		if item.Type != "function_call" {
			return nil
		}
		fc := item.AsFunctionCall()
		id := responsesToolCallID(fc)
		if id == "" {
			return nil
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if s.toolCallStarted[id] {
			return nil
		}
		s.toolCallStarted[id] = true
		if err := s.ensureMessageStarted(); err != nil {
			return err
		}
		if err := s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   fc.Name,
		}); err != nil {
			return err
		}
	case "response.function_call_arguments.delta":
		d := cur.AsResponseFunctionCallArgumentsDelta()
		id := d.ItemID
		if id == "" || d.Delta == "" {
			return nil
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			if err := s.ensureMessageStarted(); err != nil {
				return err
			}
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
			}); err != nil {
				return err
			}
		}
		s.toolCallArgDeltas[id] = true
		if err := s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      d.Delta,
		}); err != nil {
			return err
		}
	case "response.function_call_arguments.done":
		d := cur.AsResponseFunctionCallArgumentsDone()
		id := d.ItemID
		if id == "" {
			return nil
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			if err := s.ensureMessageStarted(); err != nil {
				return err
			}
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
				ToolName:   d.Name,
			}); err != nil {
				return err
			}
		}
		if !s.toolCallArgDeltas[id] && d.Arguments != "" {
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      d.Arguments,
			}); err != nil {
				return err
			}
		}
		return s.emitToolCallFinished(id)
	case "response.output_item.done":
		doneEv := cur.AsResponseOutputItemDone()
		item := doneEv.Item
		if item.Type != "function_call" {
			return nil
		}
		fc := item.AsFunctionCall()
		id := responsesToolCallID(fc)
		if id == "" {
			return nil
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			if err := s.ensureMessageStarted(); err != nil {
				return err
			}
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
				ToolName:   fc.Name,
			}); err != nil {
				return err
			}
		}
		if !s.toolCallArgDeltas[id] && fc.Arguments != "" {
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      fc.Arguments,
			}); err != nil {
				return err
			}
		}
		return s.emitToolCallFinished(id)
	}
	return nil
}

func responsesToolCallID(fc responses.ResponseFunctionToolCall) string {
	if fc.ID != "" {
		return fc.ID
	}
	return fc.CallID
}

func (s *responsesStream) ensureResponseStarted() error {
	if s.sawResp {
		return nil
	}
	s.sawResp = true
	return s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
}

func (s *responsesStream) ensureMessageStarted() error {
	if s.sawMsg {
		return nil
	}
	s.sawMsg = true
	return s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
}

func (s *responsesStream) emitToolCallFinished(id string) error {
	if s.toolCallFinished[id] {
		return nil
	}
	s.toolCallFinished[id] = true
	return s.pending.Push(lipapi.Event{
		Kind:       lipapi.EventToolCallFinished,
		ToolCallID: id,
	})
}

func (s *responsesStream) emitToolCallsFromCompletedResponse(resp responses.Response) error {
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		fc := item.AsFunctionCall()
		id := responsesToolCallID(fc)
		if id == "" || s.toolCallFinished[id] {
			continue
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			if err := s.ensureMessageStarted(); err != nil {
				return err
			}
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
				ToolName:   fc.Name,
			}); err != nil {
				return err
			}
		}
		if !s.toolCallArgDeltas[id] && fc.Arguments != "" {
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      fc.Arguments,
			}); err != nil {
				return err
			}
		}
		if err := s.emitToolCallFinished(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *responsesStream) usageFromResponse(resp responses.Response) *lipapi.Event {
	u := resp.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	ev := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens:    safecast.IntFromInt64Clamp(u.OutputTokens),
		CacheReadTokens: safecast.IntFromInt64Clamp(u.InputTokensDetails.CachedTokens),
		ReasoningTokens: safecast.IntFromInt64Clamp(u.OutputTokensDetails.ReasoningTokens),
		TotalTokens:     safecast.IntFromInt64Clamp(u.TotalTokens),
		RawUsageJSON:    rawResponsesUsageJSON(u.RawJSON(), u),
	}
	return &ev
}

func rawResponsesUsageJSON(raw string, usage any) string {
	if raw != "" {
		return raw
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *responsesStream) emitOutputMedia(resp responses.Response) error {
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		msg := item.AsMessage()
		for _, c := range msg.Content {
			raw := c.RawJSON()
			if raw == "" {
				continue
			}
			var probe struct {
				Type     string          `json:"type"`
				ImageURL json.RawMessage `json:"image_url"`
				FileID   string          `json:"file_id"`
			}
			if err := json.Unmarshal([]byte(raw), &probe); err != nil {
				continue
			}
			switch probe.Type {
			case "input_image":
				url := extractImageURL(probe.ImageURL)
				if url == "" {
					continue
				}
				if err := s.ensureResponseStarted(); err != nil {
					return err
				}
				if err := s.ensureMessageStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{
					Kind:          lipapi.EventAssistantImageRef,
					AssistantRef:  url,
					AssistantMIME: sniffImageMIME(url),
				}); err != nil {
					return err
				}
			case "input_file":
				if strings.TrimSpace(probe.FileID) == "" {
					continue
				}
				if err := s.ensureResponseStarted(); err != nil {
					return err
				}
				if err := s.ensureMessageStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{
					Kind:          lipapi.EventAssistantFileRef,
					AssistantRef:  probe.FileID,
					AssistantMIME: "application/octet-stream",
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func extractImageURL(raw json.RawMessage) string {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.URL != "" {
		return obj.URL
	}
	return ""
}

func sniffImageMIME(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, ".png"), strings.Contains(lower, "image/png"):
		return "image/png"
	case strings.Contains(lower, ".jpg"), strings.Contains(lower, ".jpeg"), strings.Contains(lower, "image/jpeg"):
		return "image/jpeg"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}

func (s *responsesStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		if s.sdk != nil {
			err = s.sdk.Close()
		}
	})
	return err
}

func (s *responsesStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
