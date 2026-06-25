package anthropicmessages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// msgStream adapts the Anthropic SSE stream to lipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on sdk.Next; Close closes the SDK stream to unblock Next.
// Context: sdk.Next does not observe ctx; cancel the request context alone may not
// return from Recv until Close runs (see [lipapi.EventStream] cancellation notes).
type msgStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *ssestream.Stream[anthropic.MessageStreamEventUnion]

	pending      stream.PendingEventQueue
	sawResp      bool
	sawMsg       bool
	terminal     bool
	activeToolID string
	closed       bool
}

func newMessageStream(s *ssestream.Stream[anthropic.MessageStreamEventUnion], maxPending int) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &msgStream{
		sdk:     s,
		pending: stream.NewPendingEventQueue(maxPending),
	}
}

func (s *msgStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
				return lipapi.Event{}, fmt.Errorf("anthropic: recv stream: %w", err)
			}
			if s.terminal {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			s.terminal = true
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
		if err := s.handleEvent(cur); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *msgStream) handleEvent(cur anthropic.MessageStreamEventUnion) error {
	switch v := cur.AsAny().(type) {
	case anthropic.MessageStartEvent:
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
	case anthropic.MessageDeltaEvent:
		if u := usageFromMessageDelta(v); u != nil {
			if err := s.pending.Push(*u); err != nil {
				return err
			}
		}
	case anthropic.ContentBlockStartEvent:
		cb := v.ContentBlock
		if media := assistantMediaEventsFromContentBlockStart(cb); len(media) > 0 {
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
			for _, e := range media {
				if err := s.pending.Push(e); err != nil {
					return err
				}
			}
		} else {
			switch cb.Type {
			case "tool_use":
				tu := cb.AsToolUse()
				s.activeToolID = tu.ID
				if err := s.pending.Push(lipapi.Event{
					Kind:       lipapi.EventToolCallStarted,
					ToolCallID: tu.ID,
					ToolName:   tu.Name,
				}); err != nil {
					return err
				}
			}
		}
	case anthropic.ContentBlockDeltaEvent:
		d := v.Delta
		switch t := d.AsAny().(type) {
		case anthropic.TextDelta:
			if t.Text != "" {
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
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: t.Text}); err != nil {
					return err
				}
			}
		case anthropic.InputJSONDelta:
			if t.PartialJSON != "" {
				if err := s.pending.Push(lipapi.Event{
					Kind:       lipapi.EventToolCallArgsDelta,
					ToolCallID: s.activeToolID,
					Delta:      t.PartialJSON,
				}); err != nil {
					return err
				}
			}
		case anthropic.ThinkingDelta:
			if t.Thinking != "" {
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: t.Thinking}); err != nil {
					return err
				}
			}
		}
	case anthropic.ContentBlockStopEvent:
		if s.activeToolID != "" {
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallFinished,
				ToolCallID: s.activeToolID,
			}); err != nil {
				return err
			}
			s.activeToolID = ""
		}
	case anthropic.MessageStopEvent:
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
			return err
		}
		s.terminal = true
	}
	return nil
}

func usageFromMessageDelta(v anthropic.MessageDeltaEvent) *lipapi.Event {
	u := v.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	ev := lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens:     safecast.IntFromInt64Clamp(u.OutputTokens),
		CacheReadTokens:  safecast.IntFromInt64Clamp(u.CacheReadInputTokens),
		CacheWriteTokens: safecast.IntFromInt64Clamp(u.CacheCreationInputTokens),
		TotalTokens:      safecast.IntFromInt64Clamp(u.InputTokens + u.OutputTokens),
		RawUsageJSON:     rawUsageJSON(u.RawJSON(), u),
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

func (s *msgStream) Close() error {
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

func (s *msgStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
