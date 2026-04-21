package openairesponses

import (
	"context"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type sdkStream struct {
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

func newSDKStream(s *ssestream.Stream[responses.ResponseStreamEventUnion]) lipapi.EventStream {
	if s == nil {
		return lipapi.FixedEventStream(nil)
	}
	return &sdkStream{
		sdk:               s,
		toolCallStarted:   map[string]bool{},
		toolCallArgDeltas: map[string]bool{},
		toolCallFinished:  map[string]bool{},
	}
}

func (s *sdkStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		if ev, ok := s.pending.PopFront(); ok {
			return ev, nil
		}
		if !s.sdk.Next() {
			if err := s.sdk.Err(); err != nil {
				return lipapi.Event{}, err
			}
			return lipapi.Event{}, io.EOF
		}
		cur := s.sdk.Current()
		s.handleUnion(cur)
	}
}

func (s *sdkStream) handleUnion(cur responses.ResponseStreamEventUnion) {
	if s.toolCallStarted == nil {
		s.toolCallStarted = map[string]bool{}
		s.toolCallArgDeltas = map[string]bool{}
		s.toolCallFinished = map[string]bool{}
	}
	switch cur.Type {
	case "response.created":
		if !s.sawResp {
			s.sawResp = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
	case "response.output_text.delta":
		if !s.sawResp {
			s.sawResp = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMsg {
			s.sawMsg = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		if cur.Delta != "" {
			s.sawTextDelta = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventTextDelta, Delta: cur.Delta})
		}
	case "response.completed":
		resp := cur.Response
		if !s.sawResp {
			s.sawResp = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMsg {
			s.sawMsg = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		if !s.sawTextDelta {
			text := resp.OutputText()
			if text != "" {
				s.pending.Push( lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text})
			}
		}
		s.emitToolCallsFromCompletedResponse(resp)
		if usage := usageFromResponse(resp); usage != nil {
			s.pending.Push( *usage)
		}
		s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseFinished})
	case "error":
		ev := cur.AsError()
		if !s.sawResp {
			s.sawResp = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		msg := ev.Message
		if msg == "" {
			msg = "stream error"
		}
		s.pending.Push( lipapi.Event{
			Kind:         lipapi.EventError,
			ErrorCode:    ev.Code,
			ErrorMessage: msg,
		})
	case "response.output_item.added":
		addEv := cur.AsResponseOutputItemAdded()
		item := addEv.Item
		if item.Type != "function_call" {
			return
		}
		fc := item.AsFunctionCall()
		id := toolCallItemID(fc)
		if id == "" {
			return
		}
		s.ensureResponseStarted()
		if s.toolCallStarted[id] {
			return
		}
		s.toolCallStarted[id] = true
		s.ensureAssistantMessageStarted()
		s.pending.Push( lipapi.Event{
			Kind:       lipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   fc.Name,
		})
	case "response.function_call_arguments.delta":
		d := cur.AsResponseFunctionCallArgumentsDelta()
		id := d.ItemID
		if id == "" || d.Delta == "" {
			return
		}
		s.ensureResponseStarted()
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			s.ensureAssistantMessageStarted()
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
			})
		}
		s.toolCallArgDeltas[id] = true
		s.pending.Push( lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      d.Delta,
		})
	case "response.function_call_arguments.done":
		d := cur.AsResponseFunctionCallArgumentsDone()
		id := d.ItemID
		if id == "" {
			return
		}
		s.ensureResponseStarted()
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			s.ensureAssistantMessageStarted()
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
				ToolName:   d.Name,
			})
		}
		if !s.toolCallArgDeltas[id] && d.Arguments != "" {
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      d.Arguments,
			})
		}
		s.emitToolCallFinished(id)
	case "response.output_item.done":
		doneEv := cur.AsResponseOutputItemDone()
		item := doneEv.Item
		if item.Type != "function_call" {
			return
		}
		fc := item.AsFunctionCall()
		id := toolCallItemID(fc)
		if id == "" {
			return
		}
		s.ensureResponseStarted()
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			s.ensureAssistantMessageStarted()
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
				ToolName:   fc.Name,
			})
		}
		if !s.toolCallArgDeltas[id] && fc.Arguments != "" {
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      fc.Arguments,
			})
		}
		s.emitToolCallFinished(id)
	default:
		// Ignore intermediate events (in_progress, queued, etc.).
	}
}

func toolCallItemID(fc responses.ResponseFunctionToolCall) string {
	if fc.ID != "" {
		return fc.ID
	}
	return fc.CallID
}

func (s *sdkStream) ensureResponseStarted() {
	if s.sawResp {
		return
	}
	s.sawResp = true
	s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
}

func (s *sdkStream) ensureAssistantMessageStarted() {
	if s.sawMsg {
		return
	}
	s.sawMsg = true
	s.pending.Push( lipapi.Event{Kind: lipapi.EventMessageStarted})
}

func (s *sdkStream) emitToolCallFinished(id string) {
	if s.toolCallFinished[id] {
		return
	}
	s.toolCallFinished[id] = true
	s.pending.Push( lipapi.Event{
		Kind:       lipapi.EventToolCallFinished,
		ToolCallID: id,
	})
}

func (s *sdkStream) emitToolCallsFromCompletedResponse(resp responses.Response) {
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		fc := item.AsFunctionCall()
		id := toolCallItemID(fc)
		if id == "" || s.toolCallFinished[id] {
			continue
		}
		s.ensureResponseStarted()
		if !s.toolCallStarted[id] {
			s.toolCallStarted[id] = true
			s.ensureAssistantMessageStarted()
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: id,
				ToolName:   fc.Name,
			})
		}
		if !s.toolCallArgDeltas[id] && fc.Arguments != "" {
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      fc.Arguments,
			})
		}
		s.emitToolCallFinished(id)
	}
}

func usageFromResponse(resp responses.Response) *lipapi.Event {
	u := resp.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  int(u.InputTokens),
		OutputTokens: int(u.OutputTokens),
	}
}

func (s *sdkStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.sdk.Close()
}
