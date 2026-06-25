package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/openairesponsestream"
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

	pending stream.PendingEventQueue
	mapper  *openairesponsestream.Mapper
	closed  bool
}

func newSDKStream(s *ssestream.Stream[responses.ResponseStreamEventUnion], maxPending int) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	st := &sdkStream{
		sdk:     s,
		pending: stream.NewPendingEventQueue(maxPending),
	}
	st.mapper = openairesponsestream.New(&st.pending)
	return st
}

func (s *sdkStream) eventMapper() *openairesponsestream.Mapper {
	if s.mapper == nil {
		s.mapper = openairesponsestream.New(&s.pending)
	}
	return s.mapper
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
	m := s.eventMapper()
	switch cur.Type {
	case "response.created":
		return m.ResponseCreated()
	case "response.output_text.delta":
		return m.OutputTextDelta(cur.Delta)
	case "response.completed":
		resp := cur.Response
		if err := m.BeginCompleted(); err != nil {
			return err
		}
		if !m.SawTextDelta() {
			if err := m.CompletedTextFallback(resp.OutputText()); err != nil {
				return err
			}
		}
		if err := emitOutputMediaFromResponse(s, resp); err != nil {
			return err
		}
		if err := s.emitToolCallsFromCompletedResponse(resp); err != nil {
			return err
		}
		if usage := usageFromResponse(resp); usage != nil {
			if err := m.PushUsage(usage); err != nil {
				return err
			}
		}
		return m.ResponseFinished()
	case "error":
		ev := cur.AsError()
		return m.StreamError(ev.Code, ev.Message, "stream error")
	case "response.output_item.added":
		addEv := cur.AsResponseOutputItemAdded()
		item := addEv.Item
		if item.Type != "function_call" {
			return nil
		}
		fc := item.AsFunctionCall()
		return m.ToolCallAdded(openairesponsestream.ToolCallID(fc.ID, fc.CallID), fc.Name)
	case "response.function_call_arguments.delta":
		d := cur.AsResponseFunctionCallArgumentsDelta()
		id := openairesponsestream.ToolCallIDFromRaw(d.ItemID, d.RawJSON())
		return m.ToolCallArgsDelta(id, d.Delta)
	case "response.function_call_arguments.done":
		d := cur.AsResponseFunctionCallArgumentsDone()
		id := openairesponsestream.ToolCallIDFromRaw(d.ItemID, d.RawJSON())
		return m.FinishToolCallArguments(id, d.Name, d.Arguments)
	case "response.output_item.done":
		doneEv := cur.AsResponseOutputItemDone()
		item := doneEv.Item
		if item.Type != "function_call" {
			return nil
		}
		fc := item.AsFunctionCall()
		return m.FinishToolCallArguments(openairesponsestream.ToolCallID(fc.ID, fc.CallID), fc.Name, fc.Arguments)
	default:
		// Ignore intermediate events (in_progress, queued, etc.).
	}
	return nil
}

func (s *sdkStream) emitToolCallsFromCompletedResponse(resp responses.Response) error {
	m := s.eventMapper()
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		fc := item.AsFunctionCall()
		if err := m.EmitCompletedToolCall(openairesponsestream.ToolCallID(fc.ID, fc.CallID), fc.Name, fc.Arguments); err != nil {
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
	ev := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens:    safecast.IntFromInt64Clamp(u.OutputTokens),
		CacheReadTokens: safecast.IntFromInt64Clamp(u.InputTokensDetails.CachedTokens),
		ReasoningTokens: safecast.IntFromInt64Clamp(u.OutputTokensDetails.ReasoningTokens),
		TotalTokens:     safecast.IntFromInt64Clamp(u.TotalTokens),
		RawUsageJSON:    rawUsageJSON(u.RawJSON(), u),
	}
	return &ev
}

func rawUsageJSON(raw string, usage any) string {
	if raw != "" {
		return raw
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
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

func (s *sdkStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
