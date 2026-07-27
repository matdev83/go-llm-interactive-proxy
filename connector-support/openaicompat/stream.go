package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type sliceStream struct {
	mu     sync.Mutex
	events []lipapi.Event
	idx    int
	closed bool
}

func (s *sliceStream) Recv(context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipapi.Event{}, fmt.Errorf("openaicompat: stream closed")
	}
	if s.idx >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type sseStream struct {
	resp     *http.Response
	flavor   Flavor
	maxBytes int64
	sc       *bufio.Scanner
	read     int64
	pending  []lipapi.Event
	started  bool
	msgStart bool
	done     bool
	closed   bool
	mu       sync.Mutex
}

func newSSEStream(resp *http.Response, flavor Flavor, maxBytes int64) *sseStream {
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxBytes))
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &sseStream{resp: resp, flavor: flavor, maxBytes: maxBytes, sc: sc}
}

func (s *sseStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return lipapi.Event{}, fmt.Errorf("openaicompat: stream closed")
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
				return lipapi.Event{}, fmt.Errorf("openaicompat: sse: %w", err)
			}
			s.done = true
			if s.started {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
				continue
			}
			return lipapi.Event{}, io.EOF
		}
		line := s.sc.Text()
		s.read += int64(len(line) + 1)
		if s.read > s.maxBytes {
			return lipapi.Event{}, fmt.Errorf("openaicompat: sse exceeded %d bytes", s.maxBytes)
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			s.done = true
			if s.started {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
			}
			continue
		}
		events, err := decodeSSEData([]byte(payload), s.flavor, &s.started, &s.msgStart)
		if err != nil {
			return lipapi.Event{}, err
		}
		s.pending = append(s.pending, events...)
	}
}

func (s *sseStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.resp != nil && s.resp.Body != nil {
		return s.resp.Body.Close()
	}
	return nil
}

func decodeSSEData(raw []byte, flavor Flavor, started, msgStart *bool) ([]lipapi.Event, error) {
	switch flavor {
	case FlavorResponses:
		return decodeResponsesSSE(raw, started, msgStart)
	default:
		return decodeChatSSE(raw, started, msgStart)
	}
}

func decodeChatSSE(raw []byte, started, msgStart *bool) ([]lipapi.Event, error) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return nil, fmt.Errorf("openaicompat: chat sse: %w", err)
	}
	var out []lipapi.Event
	ensure := func() {
		if !*started {
			*started = true
			out = append(out, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !*msgStart {
			*msgStart = true
			out = append(out, lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
	}
	for _, ch := range chunk.Choices {
		d := ch.Delta
		if r := firstNonEmpty(d.Reasoning, d.ReasoningContent); r != "" {
			ensure()
			out = append(out, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: r})
		}
		for _, tc := range d.ToolCalls {
			ensure()
			if tc.ID != "" && tc.Function.Name != "" {
				out = append(out, lipapi.Event{
					Kind: lipapi.EventToolCallStarted, ToolCallID: tc.ID, ToolName: tc.Function.Name,
				})
			}
			if tc.Function.Arguments != "" {
				out = append(out, lipapi.Event{
					Kind: lipapi.EventToolCallArgsDelta, ToolCallID: tc.ID, Delta: tc.Function.Arguments,
				})
			}
		}
		if d.Content != "" {
			ensure()
			out = append(out, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: d.Content})
		}
	}
	if chunk.Usage != nil {
		if !*started {
			*started = true
			out = append(out, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		out = append(out, lipapi.Event{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  chunk.Usage.PromptTokens,
			OutputTokens: chunk.Usage.CompletionTokens,
		})
	}
	return out, nil
}

func decodeResponsesSSE(raw []byte, started, msgStart *bool) ([]lipapi.Event, error) {
	var envelope struct {
		Type     string          `json:"type"`
		Delta    json.RawMessage `json:"delta"`
		Text     string          `json:"text"`
		Response *struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("openaicompat: responses sse: %w", err)
	}
	var out []lipapi.Event
	switch {
	case envelope.Type == "response.created" || envelope.Type == "response.started":
		if !*started {
			*started = true
			out = append(out, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
	case strings.Contains(envelope.Type, "output_text.delta"):
		if !*started {
			*started = true
			out = append(out, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !*msgStart {
			*msgStart = true
			out = append(out, lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		text := envelope.Text
		if text == "" && len(envelope.Delta) > 0 {
			_ = json.Unmarshal(envelope.Delta, &text)
			if text == "" {
				text = string(bytes.Trim(envelope.Delta, `"`))
			}
		}
		if text != "" {
			out = append(out, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text})
		}
	case envelope.Type == "response.completed":
		if envelope.Response != nil && envelope.Response.Usage != nil {
			out = append(out, lipapi.Event{
				Kind:         lipapi.EventUsageDelta,
				InputTokens:  envelope.Response.Usage.InputTokens,
				OutputTokens: envelope.Response.Usage.OutputTokens,
			})
		}
	}
	return out, nil
}

func decodeNonStream(raw []byte, flavor Flavor) ([]lipapi.Event, error) {
	started, msgStart := false, false
	switch flavor {
	case FlavorResponses:
		var resp struct {
			Output []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("openaicompat: responses json: %w", err)
		}
		out := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}}
		for _, item := range resp.Output {
			for _, c := range item.Content {
				if c.Text != "" {
					out = append(out, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: c.Text})
				}
			}
		}
		if resp.Usage != nil {
			out = append(out, lipapi.Event{
				Kind: lipapi.EventUsageDelta, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			})
		}
		out = append(out, lipapi.Event{Kind: lipapi.EventResponseFinished})
		return out, nil
	default:
		var comp struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &comp); err != nil {
			return nil, fmt.Errorf("openaicompat: chat json: %w", err)
		}
		_ = started
		_ = msgStart
		out := []lipapi.Event{{Kind: lipapi.EventResponseStarted}, {Kind: lipapi.EventMessageStarted}}
		for _, ch := range comp.Choices {
			if r := firstNonEmpty(ch.Message.Reasoning, ch.Message.ReasoningContent); r != "" {
				out = append(out, lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: r})
			}
			if ch.Message.Content != "" {
				out = append(out, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: ch.Message.Content})
			}
		}
		if comp.Usage != nil {
			out = append(out, lipapi.Event{
				Kind: lipapi.EventUsageDelta, InputTokens: comp.Usage.PromptTokens, OutputTokens: comp.Usage.CompletionTokens,
			})
		}
		out = append(out, lipapi.Event{Kind: lipapi.EventResponseFinished})
		return out, nil
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
