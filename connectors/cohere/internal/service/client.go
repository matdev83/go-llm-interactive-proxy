package service

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
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Client struct {
	Config        Config
	TokenProvider TokenProvider
	HTTPClient    *http.Client
}

type cohereMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cohereChatRequest struct {
	Model     string          `json:"model"`
	Messages  []cohereMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	MaxTokens *uint32         `json:"max_tokens,omitempty"`
}

type cohereMessageContent struct {
	Text string
}

func (mc *cohereMessageContent) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		mc.Text = str
		return nil
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &arr); err == nil {
		var sb strings.Builder
		for _, p := range arr {
			sb.WriteString(p.Text)
		}
		mc.Text = sb.String()
		return nil
	}
	return nil
}

type cohereChatResponse struct {
	ID      string `json:"id"`
	Message struct {
		Role    string               `json:"role"`
		Content cohereMessageContent `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

func sanitizeSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}

func (c *Client) Open(ctx context.Context, call lipapi.Call, targetModel string) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	if call.Invocation.Operation == lipapi.OperationOpenAIResponses ||
		call.Invocation.Operation == lipapi.OperationOpenResponsesCreate {
		return nil, fmt.Errorf("cohere: responses operations are not supported")
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("cohere: tools are not supported")
	}

	targetModel = strings.TrimSpace(targetModel)
	if targetModel == "" {
		return nil, fmt.Errorf("cohere: model is required")
	}

	var messages []cohereMessage
	for _, inst := range call.Instructions {
		var sb strings.Builder
		for _, p := range inst.Parts {
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("cohere: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
			}
			sb.WriteString(p.Text)
		}
		messages = append(messages, cohereMessage{Role: "system", Content: sb.String()})
	}

	for _, msg := range call.Messages {
		var role string
		switch msg.Role {
		case lipapi.RoleSystem:
			role = "system"
		case lipapi.RoleUser:
			role = "user"
		case lipapi.RoleAssistant:
			role = "assistant"
		default:
			return nil, fmt.Errorf("cohere: unsupported message role %q", msg.Role)
		}

		var sb strings.Builder
		for _, p := range msg.Parts {
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("cohere: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
			}
			sb.WriteString(p.Text)
		}
		messages = append(messages, cohereMessage{Role: role, Content: sb.String()})
	}

	isStreaming := call.Invocation.DeliveryMode != lipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != lipapi.TransportModeNonStreaming

	chatReq := cohereChatRequest{
		Model:    targetModel,
		Messages: messages,
		Stream:   isStreaming,
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		mt := uint32(*call.Options.MaxOutputTokens)
		chatReq.MaxTokens = &mt
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("cohere: marshal chat request: %w", err)
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("cohere: get token: %w", err)
	}

	reqURL := fmt.Sprintf("%s/v2/chat", c.Config.Origin())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("cohere: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if isStreaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	httpReq.Header.Set("Authorization", "Bearer "+tok)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cohere: do chat request: %s", sanitizeSecret(err.Error(), tok))
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("cohere: chat request failed (status %d): %s", resp.StatusCode, sanitizeSecret(string(respBody), tok))
	}

	if isStreaming {
		return lipapi.CloseOnlyManagedStream{Stream: newSSEChatStream(ctx, resp.Body)}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cohere: read chat response: %w", err)
	}

	var cr cohereChatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("cohere: unmarshal chat response: %w", err)
	}

	text := cr.Message.Content.Text
	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
	}
	if text != "" {
		events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text})
	}
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})

	return lipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

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
		return lipapi.Event{}, fmt.Errorf("cohere: stream closed")
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

type sseChatStream struct {
	ctx      context.Context
	body     io.ReadCloser
	scanner  *bufio.Scanner
	mu       sync.Mutex
	started  bool
	finished bool
	closed   bool
}

func newSSEChatStream(ctx context.Context, body io.ReadCloser) *sseChatStream {
	return &sseChatStream{
		ctx:     ctx,
		body:    body,
		scanner: bufio.NewScanner(body),
	}
}

func (s *sseChatStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return lipapi.Event{}, fmt.Errorf("cohere: stream closed")
	}

	if !s.started {
		s.started = true
		return lipapi.Event{Kind: lipapi.EventResponseStarted}, nil
	}

	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return lipapi.Event{}, err
		}
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var ev struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Message struct {
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
				Text string `json:"text"`
			} `json:"delta"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			switch ev.Type {
			case "content-delta":
				delta := ev.Delta.Message.Content.Text
				if delta == "" {
					delta = ev.Delta.Text
				}
				if delta == "" {
					delta = ev.Text
				}
				if delta != "" {
					return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: delta}, nil
				}
			case "message-end":
				if !s.finished {
					s.finished = true
					return lipapi.Event{Kind: lipapi.EventResponseFinished}, nil
				}
			default:
				if ev.Delta.Message.Content.Text != "" {
					return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: ev.Delta.Message.Content.Text}, nil
				}
				if ev.Delta.Text != "" {
					return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: ev.Delta.Text}, nil
				}
			}
		}
	}

	if err := s.scanner.Err(); err != nil && err != io.EOF {
		return lipapi.Event{}, err
	}

	if !s.finished {
		s.finished = true
		return lipapi.Event{Kind: lipapi.EventResponseFinished}, nil
	}

	return lipapi.Event{}, io.EOF
}

func (s *sseChatStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

type cohereModelItem struct {
	Name          string   `json:"name"`
	Endpoints     []string `json:"endpoints"`
	ContextLength int      `json:"context_length"`
}

type cohereListModelsResponse struct {
	Models []cohereModelItem `json:"models"`
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: get token: %w", err)
	}

	reqURL := fmt.Sprintf("%s/v1/models?endpoint=chat", c.Config.Origin())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: create list models request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: do list models request: %s", sanitizeSecret(err.Error(), tok))
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: read list models response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: list models failed (status %d): %s", resp.StatusCode, sanitizeSecret(string(bodyBytes), tok))
	}

	var lmResp cohereListModelsResponse
	if err := json.Unmarshal(bodyBytes, &lmResp); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: unmarshal list models response: %w", err)
	}

	dropPatterns := []string{"embed", "rerank", "image", "audio"}
	var out []backendplugin.ModelDescriptor

	for _, m := range lmResp.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		drop := false
		for _, p := range dropPatterns {
			if strings.Contains(lower, p) {
				drop = true
				break
			}
		}
		if drop {
			continue
		}

		if len(m.Endpoints) > 0 {
			hasChat := false
			for _, ep := range m.Endpoints {
				if strings.EqualFold(strings.TrimSpace(ep), "chat") {
					hasChat = true
					break
				}
			}
			if !hasChat {
				continue
			}
		}

		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + name,
			NativeModelID:    name,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}

	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}
