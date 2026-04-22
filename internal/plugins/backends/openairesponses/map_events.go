package openairesponses

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// sdkStream adapts the OpenAI Responses SSE stream to lipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on sdk.Next; Close closes the SDK stream to unblock Next.
// Context: sdk.Next does not observe ctx; cancel the request context alone may not
// return from Recv until Close runs (see [lipapi.EventStream] cancellation notes).
type sdkStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *ssestream.Stream[responses.ResponseStreamEventUnion]

	pending      stream.PendingEventQueue
	sawResp      bool
	sawMsg       bool
	sawTextDelta bool
	closed       bool

	// Tool-call streaming: Responses API uses output item id as the stable key.
	toolCallStarted   map[string]bool
	toolCallArgDeltas map[string]bool
	toolCallFinished  map[string]bool
}

func newSDKStream(s *ssestream.Stream[responses.ResponseStreamEventUnion], maxPending int) lipapi.EventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &sdkStream{
		sdk:               s,
		pending:           stream.NewPendingEventQueue(maxPending),
		toolCallStarted:   map[string]bool{},
		toolCallArgDeltas: map[string]bool{},
		toolCallFinished:  map[string]bool{},
	}
}

func (s *sdkStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
				return lipapi.Event{}, fmt.Errorf("openai-responses: recv stream: %w", err)
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

func (s *sdkStream) handleUnion(cur responses.ResponseStreamEventUnion) error {
	if s.toolCallStarted == nil {
		s.toolCallStarted = map[string]bool{}
		s.toolCallArgDeltas = map[string]bool{}
		s.toolCallFinished = map[string]bool{}
	}
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
		if err := emitOutputMediaFromResponse(s, resp); err != nil {
			return err
		}
		if err := s.emitToolCallsFromCompletedResponse(resp); err != nil {
			return err
		}
		if usage := usageFromResponse(resp); usage != nil {
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
		id := toolCallItemID(fc)
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
		if err := s.ensureAssistantMessageStarted(); err != nil {
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
			if err := s.ensureAssistantMessageStarted(); err != nil {
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
			if err := s.ensureAssistantMessageStarted(); err != nil {
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
		id := toolCallItemID(fc)
		if id == "" {
			return nil
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			if err := s.ensureAssistantMessageStarted(); err != nil {
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
	default:
		// Ignore intermediate events (in_progress, queued, etc.).
	}
	return nil
}

func toolCallItemID(fc responses.ResponseFunctionToolCall) string {
	if fc.ID != "" {
		return fc.ID
	}
	return fc.CallID
}

func (s *sdkStream) ensureResponseStarted() error {
	if s.sawResp {
		return nil
	}
	s.sawResp = true
	return s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
}

func (s *sdkStream) ensureAssistantMessageStarted() error {
	if s.sawMsg {
		return nil
	}
	s.sawMsg = true
	return s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
}

func (s *sdkStream) emitToolCallFinished(id string) error {
	if s.toolCallFinished[id] {
		return nil
	}
	s.toolCallFinished[id] = true
	return s.pending.Push(lipapi.Event{
		Kind:       lipapi.EventToolCallFinished,
		ToolCallID: id,
	})
}

func (s *sdkStream) emitToolCallsFromCompletedResponse(resp responses.Response) error {
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		fc := item.AsFunctionCall()
		id := toolCallItemID(fc)
		if id == "" || s.toolCallFinished[id] {
			continue
		}
		if err := s.ensureResponseStarted(); err != nil {
			return err
		}
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			if err := s.ensureAssistantMessageStarted(); err != nil {
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

func usageFromResponse(resp responses.Response) *lipapi.Event {
	u := resp.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens: safecast.IntFromInt64Clamp(u.OutputTokens),
	}
}

func (s *sdkStream) Close() error {
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
