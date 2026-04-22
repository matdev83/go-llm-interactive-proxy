package bedrock

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// recvSelectEntryHook is set from bedrock package tests to observe Recv reaching the
// blocking select on the SDK event channel (must be cleared via t.Cleanup).
var (
	recvSelectHookMu    sync.Mutex
	recvSelectEntryHook func()
)

func callRecvSelectEntryHook() {
	recvSelectHookMu.Lock()
	h := recvSelectEntryHook
	recvSelectHookMu.Unlock()
	if h != nil {
		h()
	}
}

// converseStream adapts the Bedrock ConverseStream SDK channel to lipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on the SDK event channel; Close closes the SDK stream to unblock Recv.
type converseStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *bedrockruntime.ConverseStreamEventStream
	ch  <-chan types.ConverseStreamOutput

	pending stream.PendingEventQueue

	sawResponse bool
	sawMessage  bool
	closed      bool
	afterFinish bool

	activeToolID string
}

func newConverseStream(sdk *bedrockruntime.ConverseStreamEventStream, maxPending int) lipapi.EventStream {
	if sdk == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &converseStream{
		sdk:     sdk,
		ch:      sdk.Events(),
		pending: stream.NewPendingEventQueue(maxPending),
	}
}

func (s *converseStream) Close() error {
	if s == nil || s.sdk == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		err = s.sdk.Close()
	})
	return err
}

func (s *converseStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
			if ev.Kind == lipapi.EventResponseFinished {
				s.afterFinish = true
			}
			s.mu.Unlock()
			return ev, nil
		}
		if s.afterFinish {
			s.mu.Unlock()
			return lipapi.Event{}, io.EOF
		}
		s.mu.Unlock()

		callRecvSelectEntryHook()
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case out, ok := <-s.ch:
			if !ok {
				s.mu.Lock()
				if s.closed {
					s.mu.Unlock()
					return lipapi.Event{}, io.EOF
				}
				if err := s.sdk.Err(); err != nil {
					s.mu.Unlock()
					return lipapi.Event{}, fmt.Errorf("bedrock: recv stream: %w", err)
				}
				if !s.sawResponse {
					s.sawResponse = true
					if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
						s.mu.Unlock()
						return lipapi.Event{}, err
					}
				}
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished}); err != nil {
					s.mu.Unlock()
					return lipapi.Event{}, err
				}
				s.mu.Unlock()
				continue
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				continue
			}
			if err := s.handleOutput(out); err != nil {
				s.mu.Unlock()
				return lipapi.Event{}, err
			}
			s.mu.Unlock()
		}
	}
}

func (s *converseStream) handleOutput(out types.ConverseStreamOutput) error {
	switch v := out.(type) {
	case *types.ConverseStreamOutputMemberMessageStart:
		if v.Value.Role == types.ConversationRoleAssistant {
			if !s.sawResponse {
				s.sawResponse = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
					return err
				}
			}
			if !s.sawMessage {
				s.sawMessage = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
		}
	case *types.ConverseStreamOutputMemberContentBlockStart:
		return s.handleBlockStart(v.Value)
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		return s.handleBlockDelta(v.Value)
	case *types.ConverseStreamOutputMemberContentBlockStop:
		return s.handleBlockStop(v.Value)
	case *types.ConverseStreamOutputMemberMessageStop:
		// stopReason available on v.Value.StopReason
		_ = v
	case *types.ConverseStreamOutputMemberMetadata:
		if u := v.Value.Usage; u != nil {
			// AWS ConverseStream usage uses *int32 token fields; ToInt32 returns int32, then [safecast] for int.
			inT := 0
			outT := 0
			if u.InputTokens != nil {
				inT = safecast.IntFromInt64Clamp(int64(aws.ToInt32(u.InputTokens)))
			}
			if u.OutputTokens != nil {
				outT = safecast.IntFromInt64Clamp(int64(aws.ToInt32(u.OutputTokens)))
			}
			if inT > 0 || outT > 0 {
				if err := s.pending.Push(lipapi.Event{
					Kind:         lipapi.EventUsageDelta,
					InputTokens:  inT,
					OutputTokens: outT,
				}); err != nil {
					return err
				}
			}
		}
	default:
		// ignore unknown union members
	}
	return nil
}

func (s *converseStream) handleBlockStart(ev types.ContentBlockStartEvent) error {
	switch st := ev.Start.(type) {
	case *types.ContentBlockStartMemberToolUse:
		tu := st.Value
		id := aws.ToString(tu.ToolUseId)
		name := aws.ToString(tu.Name)
		if id == "" {
			return nil
		}
		s.activeToolID = id
		if !s.sawResponse {
			s.sawResponse = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		return s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   name,
		})
	default:
		return nil
	}
}

func (s *converseStream) handleBlockDelta(ev types.ContentBlockDeltaEvent) error {
	switch d := ev.Delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		if d.Value == "" {
			return nil
		}
		if !s.sawResponse {
			s.sawResponse = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		return s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d.Value})
	case *types.ContentBlockDeltaMemberToolUse:
		if d.Value.Input == nil || *d.Value.Input == "" {
			return nil
		}
		if s.activeToolID == "" {
			return nil
		}
		if !s.sawResponse {
			s.sawResponse = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		return s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: s.activeToolID,
			Delta:      *d.Value.Input,
		})
	case *types.ContentBlockDeltaMemberReasoningContent:
		if txt := reasoningDeltaTextFromUnion(d.Value); txt != "" {
			if !s.sawResponse {
				s.sawResponse = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted}); err != nil {
					return err
				}
			}
			if !s.sawMessage {
				s.sawMessage = true
				if err := s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			return s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: txt})
		}
		return nil
	default:
		return nil
	}
}

func reasoningDeltaTextFromUnion(delta types.ReasoningContentBlockDelta) string {
	switch x := delta.(type) {
	case *types.ReasoningContentBlockDeltaMemberText:
		return x.Value
	default:
		return ""
	}
}

func (s *converseStream) handleBlockStop(ev types.ContentBlockStopEvent) error {
	if s.activeToolID != "" {
		if err := s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallFinished,
			ToolCallID: s.activeToolID,
		}); err != nil {
			return err
		}
		s.activeToolID = ""
	}
	_ = ev
	return nil
}
