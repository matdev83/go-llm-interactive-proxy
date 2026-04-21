package gemini

import (
	"context"
	"encoding/json"
	"io"
	"iter"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"google.golang.org/genai"
)

type genaiStream struct {
	next func() (*genai.GenerateContentResponse, error, bool)
	stop func()

	pending stream.PendingEventQueue

	sawResponse bool
	sawMessage  bool

	exhausted    bool
	afterFinish  bool
	closed       bool
	activeToolID string
}

func newGenaiStream(seq iter.Seq2[*genai.GenerateContentResponse, error]) lipapi.EventStream {
	next, stop := iter.Pull2(seq)
	return &genaiStream{next: next, stop: stop}
}

func (s *genaiStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	for {
		if ev, ok := s.pending.PopFront(); ok {
			return ev, nil
		}
		if s.afterFinish {
			return lipapi.Event{}, io.EOF
		}
		if s.exhausted {
			s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseFinished})
			s.afterFinish = true
			continue
		}

		resp, err, ok := s.next()
		if !ok {
			if err != nil {
				return lipapi.Event{}, err
			}
			if !s.sawResponse {
				s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
				s.sawResponse = true
			}
			s.exhausted = true
			continue
		}
		if err != nil {
			return lipapi.Event{}, err
		}
		s.handleResponse(resp)
	}
}

func (s *genaiStream) handleResponse(resp *genai.GenerateContentResponse) {
	if resp == nil {
		return
	}
	if !s.sawResponse {
		s.sawResponse = true
		s.pending.Push( lipapi.Event{Kind: lipapi.EventResponseStarted})
	}

	if u := usageEvent(resp); u != nil {
		s.pending.Push( *u)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return
	}
	parts := resp.Candidates[0].Content.Parts
	for _, part := range parts {
		if part == nil {
			continue
		}
		if part.Text != "" {
			if !s.sawMessage {
				s.sawMessage = true
				s.pending.Push( lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			if part.Thought {
				s.pending.Push( lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: part.Text})
			} else {
				s.pending.Push( lipapi.Event{Kind: lipapi.EventTextDelta, Delta: part.Text})
			}
			continue
		}
		if fc := part.FunctionCall; fc != nil {
			s.handleFunctionCall(fc)
		}
	}
}

func (s *genaiStream) handleFunctionCall(fc *genai.FunctionCall) {
	id := fc.ID
	if id == "" {
		id = "gemini-fn-" + fc.Name
	}
	if s.activeToolID != id {
		if s.activeToolID != "" {
			s.pending.Push( lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: s.activeToolID})
			s.activeToolID = ""
		}
		if !s.sawMessage {
			s.sawMessage = true
			s.pending.Push( lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		s.pending.Push( lipapi.Event{
			Kind:       lipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   fc.Name,
		})
		s.activeToolID = id
	}
	if len(fc.Args) > 0 {
		b, err := json.Marshal(fc.Args)
		if err == nil && len(b) > 0 {
			s.pending.Push( lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      string(b),
			})
		}
	}
	// Gemini often returns the full function call in one chunk; close immediately.
	if s.activeToolID != "" {
		s.pending.Push( lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: s.activeToolID})
		s.activeToolID = ""
	}
}

func usageEvent(resp *genai.GenerateContentResponse) *lipapi.Event {
	u := resp.UsageMetadata
	if u == nil {
		return nil
	}
	in := int(u.PromptTokenCount)
	out := int(u.CandidatesTokenCount)
	if in == 0 && out == 0 && u.TotalTokenCount == 0 {
		return nil
	}
	if in == 0 && out == 0 && u.TotalTokenCount > 0 {
		out = int(u.TotalTokenCount) - in
		if out < 0 {
			out = 0
		}
	}
	return &lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: in, OutputTokens: out}
}

func (s *genaiStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.stop()
	return nil
}
