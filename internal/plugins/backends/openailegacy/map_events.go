package openailegacy

import (
	"context"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

type chatStream struct {
	sdk *ssestream.Stream[openai.ChatCompletionChunk]

	pending         stream.PendingEventQueue
	sawResp         bool
	sawMsg          bool
	terminalEmitted bool
	closed          bool
	activeTools     map[int64]string
	activeToolOrder []int64
}

func newChatStream(s *ssestream.Stream[openai.ChatCompletionChunk]) lipapi.EventStream {
	if s == nil {
		return lipapi.FixedEventStream(nil)
	}
	return &chatStream{sdk: s}
}

func (s *chatStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
			if s.terminalEmitted {
				return lipapi.Event{}, io.EOF
			}
			if !s.sawResp {
				return lipapi.Event{}, io.EOF
			}
			s.terminalEmitted = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished})
			continue
		}
		cur := s.sdk.Current()
		s.handleChunk(cur)
	}
}

func (s *chatStream) handleChunk(ch openai.ChatCompletionChunk) {
	if !s.sawResp {
		s.sawResp = true
		s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
	}

	for _, choice := range ch.Choices {
		d := choice.Delta
		if d.Role != "" && !s.sawMsg {
			s.sawMsg = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
		}

		if len(d.ToolCalls) > 0 {
			if !s.sawMsg {
				s.sawMsg = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			for _, tc := range d.ToolCalls {
				if s.activeTools == nil {
					s.activeTools = make(map[int64]string)
				}
				if tc.ID != "" {
					if _, seen := s.activeTools[tc.Index]; !seen {
						s.activeToolOrder = append(s.activeToolOrder, tc.Index)
					}
					s.activeTools[tc.Index] = tc.ID
					s.pending.Push(lipapi.Event{
						Kind:       lipapi.EventToolCallStarted,
						ToolCallID: tc.ID,
						ToolName:   tc.Function.Name,
					})
				}
				if tc.Function.Arguments != "" {
					id := s.activeTools[tc.Index]
					s.pending.Push(lipapi.Event{
						Kind:       lipapi.EventToolCallArgsDelta,
						ToolCallID: id,
						Delta:      tc.Function.Arguments,
					})
				}
			}
		}

		if d.Content != "" {
			if !s.sawMsg {
				s.sawMsg = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d.Content})
		}

		if choice.FinishReason == "tool_calls" {
			for _, idx := range s.activeToolOrder {
				if id := s.activeTools[idx]; id != "" {
					s.pending.Push(lipapi.Event{
						Kind:       lipapi.EventToolCallFinished,
						ToolCallID: id,
					})
				}
			}
			s.activeTools = nil
			s.activeToolOrder = nil
		}
	}

	if ch.JSON.Usage.Valid() && (ch.Usage.PromptTokens > 0 || ch.Usage.CompletionTokens > 0 || ch.Usage.TotalTokens > 0) {
		s.pending.Push(lipapi.Event{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  int(ch.Usage.PromptTokens),
			OutputTokens: int(ch.Usage.CompletionTokens),
		})
	}
}

func (s *chatStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.sdk == nil {
		return nil
	}
	return s.sdk.Close()
}
