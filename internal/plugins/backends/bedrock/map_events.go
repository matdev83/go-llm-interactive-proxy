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

func newConverseStream(sdk *bedrockruntime.ConverseStreamEventStream) lipapi.EventStream {
	if sdk == nil {
		return lipapi.NewFixedEventStream(nil)
	}
	return &converseStream{
		sdk: sdk,
		ch:  sdk.Events(),
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
					s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
				}
				s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished})
				s.mu.Unlock()
				continue
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				continue
			}
			s.handleOutput(out)
			s.mu.Unlock()
		}
	}
}

func (s *converseStream) handleOutput(out types.ConverseStreamOutput) {
	switch v := out.(type) {
	case *types.ConverseStreamOutputMemberMessageStart:
		if v.Value.Role == types.ConversationRoleAssistant {
			if !s.sawResponse {
				s.sawResponse = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
			}
			if !s.sawMessage {
				s.sawMessage = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
		}
	case *types.ConverseStreamOutputMemberContentBlockStart:
		s.handleBlockStart(v.Value)
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		s.handleBlockDelta(v.Value)
	case *types.ConverseStreamOutputMemberContentBlockStop:
		s.handleBlockStop(v.Value)
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
				s.pending.Push(lipapi.Event{
					Kind:         lipapi.EventUsageDelta,
					InputTokens:  inT,
					OutputTokens: outT,
				})
			}
		}
	default:
		// ignore unknown union members
	}
}

func (s *converseStream) handleBlockStart(ev types.ContentBlockStartEvent) {
	switch st := ev.Start.(type) {
	case *types.ContentBlockStartMemberToolUse:
		tu := st.Value
		id := aws.ToString(tu.ToolUseId)
		name := aws.ToString(tu.Name)
		if id == "" {
			return
		}
		s.activeToolID = id
		if !s.sawResponse {
			s.sawResponse = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMessage {
			s.sawMessage = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   name,
		})
	default:
		return
	}
}

func (s *converseStream) handleBlockDelta(ev types.ContentBlockDeltaEvent) {
	switch d := ev.Delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		if d.Value == "" {
			return
		}
		if !s.sawResponse {
			s.sawResponse = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMessage {
			s.sawMessage = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		s.pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d.Value})
	case *types.ContentBlockDeltaMemberToolUse:
		if d.Value.Input == nil || *d.Value.Input == "" {
			return
		}
		if s.activeToolID == "" {
			return
		}
		if !s.sawResponse {
			s.sawResponse = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.sawMessage {
			s.sawMessage = true
			s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallArgsDelta,
			ToolCallID: s.activeToolID,
			Delta:      *d.Value.Input,
		})
	case *types.ContentBlockDeltaMemberReasoningContent:
		if txt := reasoningDeltaTextFromUnion(d.Value); txt != "" {
			if !s.sawResponse {
				s.sawResponse = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventResponseStarted})
			}
			if !s.sawMessage {
				s.sawMessage = true
				s.pending.Push(lipapi.Event{Kind: lipapi.EventMessageStarted})
			}
			s.pending.Push(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: txt})
		}
	default:
		return
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

func (s *converseStream) handleBlockStop(ev types.ContentBlockStopEvent) {
	if s.activeToolID != "" {
		s.pending.Push(lipapi.Event{
			Kind:       lipapi.EventToolCallFinished,
			ToolCallID: s.activeToolID,
		})
		s.activeToolID = ""
	}
	_ = ev
}
