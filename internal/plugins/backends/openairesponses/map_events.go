package openairesponses

import (
	"context"
	"fmt"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaiusage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/openairesponsestream"
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
	pump := stream.EventPump[responses.ResponseStreamEventUnion]{
		Lock:     &s.mu,
		Pending:  &s.pending,
		IsClosed: func() bool { return s.closed },
		Read: func() (responses.ResponseStreamEventUnion, bool, error) {
			if !s.sdk.Next() {
				if err := s.sdk.Err(); err != nil {
					return responses.ResponseStreamEventUnion{}, false, fmt.Errorf("openai-responses: recv stream: %w", err)
				}
				return responses.ResponseStreamEventUnion{}, false, nil
			}
			return s.sdk.Current(), true, nil
		},
		Handle: s.handleUnion,
		OnEOF: func() (bool, error) {
			return false, s.eventMapper().FinalizeOnEOF()
		},
	}
	return pump.Recv(ctx)
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
		if err := m.EmitCompletedOutputByIndex(resp); err != nil {
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
		switch item.Type {
		case "function_call":
			fc := item.AsFunctionCall()
			return m.ToolCallAdded(openairesponsestream.ToolCallID(fc.ID, fc.CallID), fc.Name)
		case "reasoning":
			return m.ReasoningOutputItemAdded(addEv.OutputIndex, item)
		default:
			return nil
		}
	case "response.reasoning_summary_part.added":
		ev := cur.AsResponseReasoningSummaryPartAdded()
		return m.ReasoningSummaryPartAdded(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Part.Text)
	case "response.reasoning_summary_part.done":
		ev := cur.AsResponseReasoningSummaryPartDone()
		return m.ReasoningSummaryPartDone(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Part.Text)
	case "response.reasoning_summary_text.delta":
		ev := cur.AsResponseReasoningSummaryTextDelta()
		return m.ReasoningSummaryTextDelta(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Delta)
	case "response.reasoning_summary_text.done":
		ev := cur.AsResponseReasoningSummaryTextDone()
		return m.ReasoningSummaryTextDone(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Text)
	case "response.reasoning_text.delta":
		ev := cur.AsResponseReasoningTextDelta()
		return m.ReasoningTextDelta(ev.ItemID, ev.OutputIndex, ev.ContentIndex, ev.Delta)
	case "response.reasoning_text.done":
		ev := cur.AsResponseReasoningTextDone()
		return m.ReasoningTextDone(ev.ItemID, ev.OutputIndex, ev.ContentIndex, ev.Text)
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
		switch item.Type {
		case "function_call":
			fc := item.AsFunctionCall()
			return m.FinishToolCallArguments(openairesponsestream.ToolCallID(fc.ID, fc.CallID), fc.Name, fc.Arguments)
		case "reasoning":
			return m.ReasoningOutputItemDone(doneEv.OutputIndex, item)
		default:
			return nil
		}
	default:
		// Ignore intermediate events (in_progress, queued, etc.).
	}
	return nil
}

func usageFromResponse(resp responses.Response) *lipapi.Event {
	if !resp.JSON.Usage.Valid() {
		return nil
	}
	ev := openaiusage.ResponsesUsageEvent(resp.Usage)
	return &ev
}

func (s *sdkStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.mapper != nil {
		s.mapper.AbortReasoningAssembly()
	}
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
