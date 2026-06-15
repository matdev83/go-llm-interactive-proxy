package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

type chatStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *ssestream.Stream[openai.ChatCompletionChunk]

	pending         stream.PendingEventQueue
	sawResp         bool
	sawMsg          bool
	terminalEmitted bool
	closed          bool
	activeTools     map[int64]string
	activeToolOrder []int64
}

func newChatStream(s *ssestream.Stream[openai.ChatCompletionChunk], maxPending int) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &chatStream{
		sdk:     s,
		pending: stream.NewPendingEventQueue(maxPending),
	}
}

func (s *chatStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
				return lipapi.Event{}, fmt.Errorf("openrouter: recv chat stream: %w", err)
			}
			if s.terminalEmitted {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			if !s.sawResp {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			s.terminalEmitted = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, err
			}
			s.mu.Unlock()
			continue
		}
		cur := s.sdk.Current()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.handleChunk(cur); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *chatStream) handleChunk(ch openai.ChatCompletionChunk) error {
	if !s.sawResp {
		s.sawResp = true
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
			return err
		}
	}

	for _, choice := range ch.Choices {
		d := choice.Delta
		if d.Role != "" && !s.sawMsg {
			s.sawMsg = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}

		if len(d.ToolCalls) > 0 {
			if !s.sawMsg {
				s.sawMsg = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
					return err
				}
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
					if err := s.pending.Push(lipapi.Event{
						Kind:       lipapi.EventToolCallStarted,
						ToolCallID: tc.ID,
						ToolName:   tc.Function.Name,
					}); err != nil {
						return err
					}
				}
				if tc.Function.Arguments != "" {
					id := s.activeTools[tc.Index]
					if err := s.pending.Push(lipapi.Event{
						Kind:       lipapi.EventToolCallArgsDelta,
						ToolCallID: id,
						Delta:      tc.Function.Arguments,
					}); err != nil {
						return err
					}
				}
			}
		}

		if d.Content != "" {
			if !s.sawMsg {
				s.sawMsg = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d.Content}); err != nil {
				return err
			}
		}

		if choice.FinishReason == "tool_calls" {
			for _, idx := range s.activeToolOrder {
				if id := s.activeTools[idx]; id != "" {
					if err := s.pending.Push(lipapi.Event{
						Kind:       lipapi.EventToolCallFinished,
						ToolCallID: id,
					}); err != nil {
						return err
					}
				}
			}
			s.activeTools = nil
			s.activeToolOrder = nil
		}
	}

	if ch.JSON.Usage.Valid() && (ch.Usage.PromptTokens > 0 || ch.Usage.CompletionTokens > 0 || ch.Usage.TotalTokens > 0) {
		ev := lipapi.Event{
			Kind:            lipapi.EventUsageDelta,
			InputTokens:     safecast.IntFromInt64Clamp(ch.Usage.PromptTokens),
			OutputTokens:    safecast.IntFromInt64Clamp(ch.Usage.CompletionTokens),
			CacheReadTokens: safecast.IntFromInt64Clamp(ch.Usage.PromptTokensDetails.CachedTokens),
			ReasoningTokens: safecast.IntFromInt64Clamp(ch.Usage.CompletionTokensDetails.ReasoningTokens),
			TotalTokens:     safecast.IntFromInt64Clamp(ch.Usage.TotalTokens),
			RawUsageJSON:    rawChatUsageJSON(ch.Usage.RawJSON(), ch.Usage),
		}
		if err := s.pending.Push(ev); err != nil {
			return err
		}
	}
	return nil
}

func rawChatUsageJSON(raw string, usage any) string {
	if raw != "" {
		return raw
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *chatStream) Close() error {
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

func (s *chatStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
