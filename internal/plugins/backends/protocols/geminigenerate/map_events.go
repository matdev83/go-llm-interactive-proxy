package geminigenerate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
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

	// backendID prefixes stream-recv errors so failures attribute to the configured
	// backend instance (hosted "gemini" or a custom-compatible instance prefix).
	backendID string
	pending   stream.PendingEventQueue

	sawResponse bool
	sawMessage  bool

	exhausted    bool
	afterFinish  bool
	closed       bool
	activeToolID string
	*coremetering.ProviderEvidenceBuffer
}

func newGenaiStream(seq iter.Seq2[*genai.GenerateContentResponse, error], backendID string, maxPending int) lipapi.ManagedEventStream {
	next, stop := iter.Pull2(seq)
	return &genaiStream{
		next:                   next,
		stop:                   stop,
		backendID:              backendID,
		pending:                stream.NewPendingEventQueue(maxPending),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
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
				return lipapi.Event{}, fmt.Errorf("%s: recv stream: %w", s.backendID, err)
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
			return lipapi.Event{}, fmt.Errorf("%s: recv stream: %w", s.backendID, err)
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
		if s.ProviderEvidenceBuffer != nil {
			s.Add(geminiEvidenceDraft(*u, resp.UsageMetadata))
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
	// genai reports usage as integer counts; reject malformed negative fields
	// before clamping to the canonical int representation.
	in, inputPresent := geminiNonNegativeInt(int64(u.PromptTokenCount))
	total, totalPresent := geminiNonNegativeInt(int64(u.TotalTokenCount))
	cache, cachePresent := geminiNonNegativeInt(int64(u.CachedContentTokenCount))
	reasoning, reasoningPresent := geminiNonNegativeInt(int64(u.ThoughtsTokenCount))
	candidates, candidatesPresent := geminiNonNegativeInt(int64(u.CandidatesTokenCount))
	out := candidates
	outputPresent := candidatesPresent
	if candidatesPresent && reasoningPresent {
		if int64(candidates) > int64(^uint(0)>>1)-int64(reasoning) {
			out, outputPresent = 0, false
		} else {
			out += reasoning
		}
	} else if !outputPresent {
		out, outputPresent = reasoning, reasoningPresent
	}
	if !inputPresent && !outputPresent && !totalPresent && !cachePresent && !reasoningPresent {
		// A usage object whose only counters are malformed is not evidence.
		return nil
	}
	if out == 0 && u.CandidatesTokenCount >= 0 && u.ThoughtsTokenCount >= 0 && totalPresent && inputPresent && total > in {
		diff := int64(total) - int64(in)
		// Gemini's total includes tokens returned by grounded/server-side tool
		// execution. Those are input-side native evidence, not generated output;
		// remove a reported non-zero tool count before deriving the legacy output
		// convenience counter.
		if u.ToolUsePromptTokenCount > 0 {
			tool := int64(u.ToolUsePromptTokenCount)
			if diff < tool {
				outputPresent = false
			} else {
				diff -= tool
			}
		}
		if !outputPresent || diff < 0 {
			out = 0
		} else {
			out = safecast.IntFromInt64Clamp(diff)
		}
	}
	ev := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: in, OutputTokens: out}
	ev.UsagePresence = lipapi.UsagePresence{InputTokens: inputPresent, OutputTokens: outputPresent, TotalTokens: totalPresent, CacheReadTokens: cachePresent, ReasoningTokens: reasoningPresent}
	ev.CacheReadTokens = cache
	ev.ReasoningTokens = reasoning
	ev.TotalTokens = total
	ev.RawUsageJSON = rawUsageJSON(u)
	ev.Accounting = geminiUsageAccounting(u)
	return &ev
}

func geminiNonNegativeInt(value int64) (int, bool) {
	if value < 0 {
		return 0, false
	}
	converted := safecast.IntFromInt64Clamp(value)
	if int64(converted) != value {
		return 0, false
	}
	return converted, true
}

func geminiUsageAccounting(u *genai.GenerateContentResponseUsageMetadata) lipapi.UsageAccountingMetadata {
	if u == nil {
		return lipapi.UsageAccountingMetadata{}
	}
	return lipapi.UsageAccountingMetadata{
		Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
		Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "gemini.generate.usage:stream",
		ServiceContext: strings.TrimSpace(string(u.TrafficType)),
	}
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

func (s *genaiStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
