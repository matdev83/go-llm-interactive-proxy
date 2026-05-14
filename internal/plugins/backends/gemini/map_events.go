package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"google.golang.org/genai"
)

// genaiStream adapts iter.Pull2 over GenAI stream responses to lipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on the iterator pull; Close invokes stop to unblock the iterator.
// Context: the pull does not observe ctx; cancel the request context alone may not
// return from Recv until Close runs (see [lipapi.EventStream] cancellation notes).
type genaiStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

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

func newGenaiStream(seq iter.Seq2[*genai.GenerateContentResponse, error], maxPending int) lipapi.ManagedEventStream {
	next, stop := iter.Pull2(seq)
	return &genaiStream{
		next:    next,
		stop:    stop,
		pending: stream.NewPendingEventQueue(maxPending),
	}
}

func (s *genaiStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
		if s.afterFinish {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		if s.exhausted {
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, err
			}
			s.afterFinish = true
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()

		resp, err, ok := s.next()

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if !ok {
			if err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, fmt.Errorf("gemini: recv stream: %w", err)
			}
			if !s.sawResponse {
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
					s.mu.Unlock()
					return lipapi.Event{}, err
				}
				s.sawResponse = true
			}
			s.exhausted = true
			s.mu.Unlock()
			continue
		}
		if err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, fmt.Errorf("gemini: recv stream: %w", err)
		}
		if err := s.handleResponse(resp); err != nil {
			s.mu.Unlock()
			return lipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *genaiStream) handleResponse(resp *genai.GenerateContentResponse) error {
	if resp == nil {
		return nil
	}
	if !s.sawResponse {
		s.sawResponse = true
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
			return err
		}
	}

	if u := usageEvent(resp); u != nil {
		if err := s.pending.Push(*u); err != nil {
			return err
		}
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil
	}
	parts := resp.Candidates[0].Content.Parts
	for _, part := range parts {
		if part == nil {
			continue
		}
		if part.Text != "" {
			if !s.sawMessage {
				s.sawMessage = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			if part.Thought {
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: part.Text}); err != nil {
					return err
				}
			} else {
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: part.Text}); err != nil {
					return err
				}
			}
			continue
		}
		if fc := part.FunctionCall; fc != nil {
			if err := s.handleFunctionCall(fc); err != nil {
				return err
			}
			continue
		}
		if fd := part.FileData; fd != nil && strings.TrimSpace(fd.FileURI) != "" {
			if !s.sawMessage {
				s.sawMessage = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			uri := strings.TrimSpace(fd.FileURI)
			mime := strings.TrimSpace(fd.MIMEType)
			if strings.HasPrefix(strings.ToLower(mime), "image/") {
				if err := s.pending.Push(lipapi.Event{
					Kind:          lipapi.EventAssistantImageRef,
					AssistantRef:  uri,
					AssistantMIME: mime,
				}); err != nil {
					return err
				}
			} else {
				if err := s.pending.Push(lipapi.Event{
					Kind:          lipapi.EventAssistantFileRef,
					AssistantRef:  uri,
					AssistantMIME: mime,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *genaiStream) handleFunctionCall(fc *genai.FunctionCall) error {
	id := fc.ID
	if id == "" {
		id = "gemini-fn-" + fc.Name
	}
	if s.activeToolID != id {
		if s.activeToolID != "" {
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: s.activeToolID}); err != nil {
				return err
			}
			s.activeToolID = ""
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		if err := s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   fc.Name,
		}); err != nil {
			return err
		}
		s.activeToolID = id
	}
	if len(fc.Args) > 0 {
		b, err := json.Marshal(fc.Args)
		if err != nil {
			return fmt.Errorf("gemini: marshal tool arguments: %w", err)
		}
		if len(b) > 0 {
			if err := s.pending.Push(lipapi.Event{
				Kind:       lipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      string(b),
			}); err != nil {
				return err
			}
		}
	}
	if s.activeToolID != "" {
		if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: s.activeToolID}); err != nil {
			return err
		}
		s.activeToolID = ""
	}
	return nil
}

func usageEvent(resp *genai.GenerateContentResponse) *lipapi.Event {
	u := resp.UsageMetadata
	if u == nil {
		return nil
	}
	// genai reports usage as integer counts; clamp to int for [lipapi.Event] (same as other backends).
	in := safecast.IntFromInt64Clamp(int64(u.PromptTokenCount))
	outputTokens := int64(u.CandidatesTokenCount) + int64(u.ThoughtsTokenCount)
	out := safecast.IntFromInt64Clamp(outputTokens)
	if in == 0 && out == 0 && u.TotalTokenCount == 0 {
		return nil
	}
	if out == 0 && u.TotalTokenCount > u.PromptTokenCount {
		diff := int64(u.TotalTokenCount) - int64(u.PromptTokenCount)
		if diff < 0 {
			out = 0
		} else {
			out = safecast.IntFromInt64Clamp(diff)
		}
	}
	ev := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: in, OutputTokens: out}
	ev.CacheReadTokens = safecast.IntFromInt64Clamp(int64(u.CachedContentTokenCount))
	ev.ReasoningTokens = safecast.IntFromInt64Clamp(int64(u.ThoughtsTokenCount))
	ev.TotalTokens = safecast.IntFromInt64Clamp(int64(u.TotalTokenCount))
	ev.RawUsageJSON = rawUsageJSON(u)
	return &ev
}

func rawUsageJSON(usage any) string {
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *genaiStream) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
	})
	return nil
}

func (s *genaiStream) Cancel(context.Context, leglifecycle.CancelCause) leglifecycle.CancelResult {
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeCloseOnly}
}
