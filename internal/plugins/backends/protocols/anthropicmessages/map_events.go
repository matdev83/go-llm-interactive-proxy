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
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
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

	// backendID prefixes stream-recv errors so failures attribute to the configured
	// backend instance (hosted "anthropic" or a custom-compatible instance prefix).
	backendID    string
	pending      stream.PendingEventQueue
	sawResp      bool
	sawMsg       bool
	terminal     bool
	activeToolID string
	closed       bool

	// cache, when non-nil, collects foreground cache evidence and issues one
	// renewable observation on the committed terminal. It is wired only for
	// automatic enrollment; nil keeps the stream observation-neutral.
	cache *cacheStreamState
}

// cacheStreamState carries the bounded observation buffer and the plugin-owned
// hook used to issue a renewable target from committed cache evidence.
type cacheStreamState struct {
	hook     CacheObservationHook
	lineage  promptcache.ObservationLineage
	renewal  RenewalSnapshot
	ttl      string
	evidence promptcache.CacheEvidence
	buffer   promptcache.ObservationBuffer
}

func newMessageStream(s *ssestream.Stream[anthropic.MessageStreamEventUnion], backendID string, maxPending int) lipapi.ManagedEventStream {
	return newMessageStreamWithCache(s, backendID, maxPending, nil)
}

func newMessageStreamWithCache(s *ssestream.Stream[anthropic.MessageStreamEventUnion], backendID string, maxPending int, cache *cacheStreamState) lipapi.ManagedEventStream {
	if s == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &msgStream{
		sdk:       s,
		backendID: backendID,
		pending:   stream.NewPendingEventQueue(maxPending),
		cache:     cache,
	}
}

func (s *msgStream) DrainPromptCacheObservations() []promptcache.Observation {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.buffer.DrainPromptCacheObservations()
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
				return lipapi.Event{}, fmt.Errorf("%s: recv stream: %w", s.backendID, err)
			}
			if s.terminal {
				s.mu.Unlock()
				return lipapi.Event{}, io.EOF
			}
			s.terminal = true
			s.completeCacheObservation()
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
			if s.cache != nil {
				s.captureEvidence(*u)
			}
		}
	case anthropic.ContentBlockStartEvent:
		cb := v.ContentBlock
		if media := assistantMediaEventsFromContentBlockStart(cb); len(media) > 0 {
			if err := s.ensureFrameStarted(); err != nil {
				return err
			}
			for _, e := range media {
				if err := s.pending.Push(e); err != nil {
					return err
				}
			}
		} else {
			switch cb.Type {
			case "thinking", "reasoning":
				thinking := cb.Thinking
				if thinking == "" && cb.Type == "reasoning" {
					var raw struct {
						Reasoning string `json:"reasoning"`
						Text      string `json:"text"`
					}
					if err := json.Unmarshal([]byte(cb.RawJSON()), &raw); err == nil {
						thinking = raw.Reasoning
						if thinking == "" {
							thinking = raw.Text
						}
					}
				}
				if thinking != "" || cb.Signature != "" {
					if err := s.ensureFrameStarted(); err != nil {
						return err
					}
				}
				if thinking != "" {
					if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: thinking}); err != nil {
						return err
					}
				}
				if cb.Signature != "" {
					if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: cb.Signature}); err != nil {
						return err
					}
				}
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
			case "redacted_thinking":
				rt := cb.AsRedactedThinking()
				opaque, err := json.Marshal(map[string]string{
					"type": "redacted_thinking",
					"data": rt.Data,
				})
				if err != nil {
					return fmt.Errorf("anthropic: redacted_thinking opaque: %w", err)
				}
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{
					Kind:   lipapi.EventReasoningOpaqueDelta,
					Opaque: opaque,
				}); err != nil {
					return err
				}
			}
		}
	case anthropic.ContentBlockDeltaEvent:
		d := v.Delta
		if d.Type == "reasoning_delta" {
			thinking := d.Thinking
			if thinking == "" {
				var raw struct {
					Reasoning string `json:"reasoning"`
					Text      string `json:"text"`
				}
				if err := json.Unmarshal([]byte(d.RawJSON()), &raw); err == nil {
					thinking = raw.Reasoning
					if thinking == "" {
						thinking = raw.Text
					}
				}
			}
			if thinking != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: thinking}); err != nil {
					return err
				}
			}
			break
		}
		switch t := d.AsAny().(type) {
		case anthropic.TextDelta:
			if t.Text != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
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
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: t.Thinking}); err != nil {
					return err
				}
			}
		case anthropic.SignatureDelta:
			if t.Signature != "" {
				if err := s.ensureFrameStarted(); err != nil {
					return err
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: t.Signature}); err != nil {
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
		s.completeCacheObservation()
	}
	return nil
}

// ensureFrameStarted emits ResponseStarted and MessageStarted if not already seen,
// so content-class deltas and assistant media refs are never published before the
// message frame. Mirrors the defensive establishment the TextDelta path already did
// inline; shared here so ThinkingDelta and SignatureDelta behave consistently.
func (s *msgStream) ensureFrameStarted() error {
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
	return nil
}

func usageFromMessageDelta(v anthropic.MessageDeltaEvent) *lipapi.Event {
	u := v.Usage
	presence := lipapi.UsagePresence{
		InputTokens:      u.JSON.InputTokens.Valid(),
		OutputTokens:     u.JSON.OutputTokens.Valid(),
		CacheReadTokens:  u.JSON.CacheReadInputTokens.Valid(),
		CacheWriteTokens: u.JSON.CacheCreationInputTokens.Valid(),
	}
	if !presence.Any() && u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	ev := lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens:     safecast.IntFromInt64Clamp(u.OutputTokens),
		CacheReadTokens:  safecast.IntFromInt64Clamp(u.CacheReadInputTokens),
		CacheWriteTokens: safecast.IntFromInt64Clamp(u.CacheCreationInputTokens),
		TotalTokens:      safecast.IntFromInt64Clamp(u.InputTokens + u.OutputTokens),
		UsagePresence:    presence,
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
