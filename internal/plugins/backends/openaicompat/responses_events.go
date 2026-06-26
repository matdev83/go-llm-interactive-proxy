package openaicompat

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

var _ lipapi.ManagedEventStream = (*responsesStream)(nil)

type responsesStream struct {
	noCopy noCopy

	mu        sync.Mutex
	closeOnce sync.Once

	provider string
	sdk      *ssestream.Stream[responses.ResponseStreamEventUnion]

	pending         stream.PendingEventQueue
	mapper          *openairesponsestream.Mapper
	terminalEmitted bool
	closed          bool
}

func NewResponsesStream(provider string, s *ssestream.Stream[responses.ResponseStreamEventUnion], maxPending int) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	st := &responsesStream{
		provider: provider,
		sdk:      s,
		pending:  stream.NewPendingEventQueue(maxPending),
	}
	st.mapper = openairesponsestream.New(&st.pending)
	return st
}

func newUnitResponsesStream() *responsesStream {
	st := &responsesStream{
		pending: stream.NewPendingEventQueue(0),
	}
	st.mapper = openairesponsestream.New(&st.pending)
	return st
}

func (s *responsesStream) Recv(ctx context.Context) (lipapi.Event, error) {
	pump := stream.EventPump[responses.ResponseStreamEventUnion]{
		Lock:     &s.mu,
		Pending:  &s.pending,
		IsClosed: func() bool { return s.closed },
		Read: func() (responses.ResponseStreamEventUnion, bool, error) {
			if !s.sdk.Next() {
				if err := s.sdk.Err(); err != nil {
					return responses.ResponseStreamEventUnion{}, false, fmt.Errorf("%s: recv responses stream: %w", s.provider, err)
				}
				return responses.ResponseStreamEventUnion{}, false, nil
			}
			return s.sdk.Current(), true, nil
		},
		Handle: s.handleUnion,
		OnEOF: func() (bool, error) {
			return true, s.finishOnEOF()
		},
	}
	return pump.Recv(ctx)
}

func (s *responsesStream) handleUnion(cur responses.ResponseStreamEventUnion) error {
	m := s.mapper
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
		if err := openairesponsestream.EmitOutputMediaFromResponse(m, &s.pending, resp); err != nil {
			return err
		}
		if err := s.emitToolCallsFromCompletedResponse(resp); err != nil {
			return err
		}
		if usage := s.usageFromResponse(resp); usage != nil {
			if err := m.PushUsage(usage); err != nil {
				return err
			}
		}
		if err := m.ResponseFinished(); err != nil {
			return err
		}
		s.terminalEmitted = true
		return nil
	case "error":
		ev := cur.AsError()
		return m.StreamError(ev.Code, ev.Message, "stream error")
	case "response.output_item.added":
		item := cur.AsResponseOutputItemAdded().Item
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
		item := cur.AsResponseOutputItemDone().Item
		if item.Type != "function_call" {
			return nil
		}
		fc := item.AsFunctionCall()
		return m.FinishToolCallArguments(
			openairesponsestream.ToolCallID(fc.ID, fc.CallID),
			fc.Name,
			fc.Arguments,
		)
	}
	return nil
}

func (s *responsesStream) finishOnEOF() error {
	if s.closed {
		return nil
	}
	if s.terminalEmitted {
		s.closed = true
		return nil
	}
	if !s.mapper.SawResponseStarted() {
		s.closed = true
		return nil
	}
	s.terminalEmitted = true
	return s.mapper.ResponseFinished()
}

func (s *responsesStream) emitToolCallsFromCompletedResponse(resp responses.Response) error {
	m := s.mapper
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		fc := item.AsFunctionCall()
		if err := m.EmitCompletedToolCall(
			openairesponsestream.ToolCallID(fc.ID, fc.CallID),
			fc.Name,
			fc.Arguments,
		); err != nil {
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
	ev := openaiusage.ResponsesUsageEvent(u)
	return &ev
}

func ResponseEvents(resp responses.Response) ([]lipapi.Event, error) {
	s := newUnitResponsesStream()
	cur := responses.ResponseStreamEventUnion{Type: "response.completed", Response: resp}
	if err := s.handleUnion(cur); err != nil {
		return nil, fmt.Errorf("responses events: %w", err)
	}
	return stream.DrainPending(&s.pending), nil
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
