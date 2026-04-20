package openairesponses

import (
	"context"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type sdkStream struct {
	sdk *ssestream.Stream[responses.ResponseStreamEventUnion]

	pending      []lipapi.Event
	sawResp      bool
	sawMsg       bool
	sawTextDelta bool
	closed       bool
}

func newSDKStream(s *ssestream.Stream[responses.ResponseStreamEventUnion]) lipapi.EventStream {
	if s == nil {
		return lipapi.FixedEventStream(nil)
	}
	return &sdkStream{sdk: s}
}

func (s *sdkStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
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
	switch cur.Type {
	case "response.created":
		if !s.sawResp {
			s.sawResp = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
	case "response.output_text.delta":
		if !s.sawResp {
			s.sawResp = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMsg {
			s.sawMsg = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		if cur.Delta != "" {
			s.sawTextDelta = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: cur.Delta})
		}
	case "response.completed":
		resp := cur.Response
		if !s.sawResp {
			s.sawResp = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMsg {
			s.sawMsg = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		if !s.sawTextDelta {
			text := resp.OutputText()
			if text != "" {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text})
			}
		}
		if usage := usageFromResponse(resp); usage != nil {
			s.pending = append(s.pending, *usage)
		}
		s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
	case "error":
		ev := cur.AsError()
		if !s.sawResp {
			s.sawResp = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		msg := ev.Message
		if msg == "" {
			msg = "stream error"
		}
		s.pending = append(s.pending, lipapi.Event{
			Kind:         lipapi.EventError,
			ErrorCode:    ev.Code,
			ErrorMessage: msg,
		})
	default:
		// Ignore intermediate events (in_progress, queued, etc.).
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
