package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type managedSSEStream struct {
	resp      *http.Response
	sc        *bufio.Scanner
	mu        sync.Mutex
	closed    bool
	started   bool
	msgStart  bool
	finished  bool
	done      bool
	pending   []lipapi.Event
	toolCalls map[int]string
}

func newManagedSSEStream(resp *http.Response) *managedSSEStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &managedSSEStream{resp: resp, sc: sc, toolCalls: make(map[int]string)}
}

type ssePayload struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *managedSSEStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipapi.Event{}, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return lipapi.Event{}, err
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
		if s.done {
			return lipapi.Event{}, io.EOF
		}
		if !s.sc.Scan() {
			if err := s.sc.Err(); err != nil {
				return lipapi.Event{}, err
			}
			s.done = true
			s.emitFinishIfStarted("")
			if len(s.pending) > 0 {
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return ev, nil
			}
			return lipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			s.done = true
			s.emitFinishIfStarted("")
			if len(s.pending) > 0 {
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return ev, nil
			}
			continue
		}
		var p ssePayload
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			continue
		}
		if err := s.handlePayload(p); err != nil {
			return lipapi.Event{}, err
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
	}
}

func (s *managedSSEStream) ensureStarted() {
	if !s.started {
		s.started = true
		s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
	}
	if !s.msgStart {
		s.msgStart = true
		s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventMessageStarted})
	}
}

func (s *managedSSEStream) emitFinishIfStarted(reason string) {
	if s.finished || !s.started {
		return
	}
	s.finished = true
	s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: reason})
}

func (s *managedSSEStream) handlePayload(p ssePayload) error {
	switch p.Type {
	case "message_start":
		if !s.started {
			s.started = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		u := p.Message.Usage
		if u.InputTokens > 0 || u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
			s.pending = append(s.pending, lipapi.Event{
				Kind: lipapi.EventUsageDelta, InputTokens: u.InputTokens,
				CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens,
			})
		}
	case "content_block_start":
		s.ensureStarted()
		if p.ContentBlock.Type == "tool_use" {
			toolID := p.ContentBlock.ID
			s.toolCalls[p.Index] = toolID
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventToolCallStarted, ToolCallID: toolID, ToolName: p.ContentBlock.Name})
		}
	case "content_block_delta":
		s.ensureStarted()
		switch p.Delta.Type {
		case "text_delta":
			if p.Delta.Text != "" {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: p.Delta.Text})
			}
		case "input_json_delta":
			if p.Delta.PartialJSON != "" {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: s.toolCalls[p.Index], Delta: p.Delta.PartialJSON})
			}
		case "thinking_delta":
			if p.Delta.Thinking != "" {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: p.Delta.Thinking})
			}
		case "signature_delta":
			if p.Delta.Signature != "" {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: p.Delta.Signature})
			}
		}
	case "content_block_stop":
		if toolID, ok := s.toolCalls[p.Index]; ok {
			delete(s.toolCalls, p.Index)
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventToolCallFinished, ToolCallID: toolID})
		}
	case "message_delta":
		if p.Usage.OutputTokens > 0 {
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventUsageDelta, OutputTokens: p.Usage.OutputTokens})
		}
		if p.Delta.StopReason != "" {
			if s.finished {
				break
			}
			s.finished = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: p.Delta.StopReason})
		}
	case "message_stop":
		s.done = true
		s.emitFinishIfStarted("")
	case "error":
		return fmt.Errorf("anthropic stream error: %s (%s)", p.Error.Message, p.Error.Type)
	}
	return nil
}

func (s *managedSSEStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	err := s.Close()
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly, Err: err}
}

func (s *managedSSEStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.resp != nil && s.resp.Body != nil {
		return s.resp.Body.Close()
	}
	return nil
}
