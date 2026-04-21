package anthropic

import (
	"context"
	"io"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type msgStream struct {
	sdk *ssestream.Stream[anthropic.MessageStreamEventUnion]

	pending      stream.PendingEventQueue
	sawResp      bool
	sawMsg       bool
	terminal     bool
	activeToolID string
	closed       bool
}

func newMessageStream(s *ssestream.Stream[anthropic.MessageStreamEventUnion]) lipapi.EventStream {
	if s == nil {
		return lipapi.FixedEventStream(nil)
	}
	return &msgStream{sdk: s}
}

func (s *msgStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
			if s.terminal {
				return lipapi.Event{}, io.EOF
			}
			s.terminal = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished})
			continue
		}
		cur := s.sdk.Current()
		s.handleEvent(cur)
	}
}

func (s *msgStream) handleEvent(cur anthropic.MessageStreamEventUnion) {
	switch v := cur.AsAny().(type) {
	case anthropic.MessageStartEvent:
		if !s.sawResp {
			s.sawResp = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMsg {
			s.sawMsg = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
	case anthropic.MessageDeltaEvent:
		if u := usageFromMessageDelta(v); u != nil {
			s.pending.Push(*u)
		}
	case anthropic.ContentBlockStartEvent:
		cb := v.ContentBlock
		if media := assistantMediaEventsFromContentBlockStart(cb); len(media) > 0 {
			if !s.sawResp {
				s.sawResp = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
			}
			if !s.sawMsg {
				s.sawMsg = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			for _, e := range media {
				s.pending.Push(e)
			}
		} else {
			switch cb.Type {
			case "tool_use":
				tu := cb.AsToolUse()
				s.activeToolID = tu.ID
				s.pending.Push(lipapi.Event{
					Kind:       lipapi.EventToolCallStarted,
					ToolCallID: tu.ID,
					ToolName:   tu.Name,
				})
			}
		}
	case anthropic.ContentBlockDeltaEvent:
		d := v.Delta
		switch t := d.AsAny().(type) {
		case anthropic.TextDelta:
			if t.Text != "" {
				if !s.sawResp {
					s.sawResp = true
					s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
				}
				if !s.sawMsg {
					s.sawMsg = true
					s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
				}
				s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: t.Text})
			}
		case anthropic.InputJSONDelta:
			if t.PartialJSON != "" {
				s.pending.Push(lipapi.Event{
					Kind:       lipapi.EventToolCallArgsDelta,
					ToolCallID: s.activeToolID,
					Delta:      t.PartialJSON,
				})
			}
		case anthropic.ThinkingDelta:
			if t.Thinking != "" {
				s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: t.Thinking})
			}
		}
	case anthropic.ContentBlockStopEvent:
		if s.activeToolID != "" {
			s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallFinished,
				ToolCallID: s.activeToolID,
			})
			s.activeToolID = ""
		}
	case anthropic.MessageStopEvent:
		s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished})
		s.terminal = true
	}
}

func usageFromMessageDelta(v anthropic.MessageDeltaEvent) *lipapi.Event {
	u := v.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	return &lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  int(u.InputTokens),
		OutputTokens: int(u.OutputTokens),
	}
}

func (s *msgStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.sdk == nil {
		return nil
	}
	return s.sdk.Close()
}
