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

type GenericChatResponseWrapper struct {
	ChatResponse *GenericChatResponse `json:"chatResponse"`
	Choices      []ChatChoice         `json:"choices"`
}

type GenericChatResponse struct {
	APIFormat string       `json:"apiFormat"`
	Choices   []ChatChoice `json:"choices"`
}

type ChatChoice struct {
	Index   int          `json:"index"`
	Message *ChatMessage `json:"message"`
	Text    string       `json:"text"`
}

type ChatMessage struct {
	Role    string        `json:"role"`
	Content []ChatContent `json:"content"`
}

type ChatContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func extractTextFromChatResponse(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", fmt.Errorf("oci-generative-ai: empty response body")
	}

	var parsed GenericChatResponseWrapper
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("oci-generative-ai: unmarshal chat response: %w", err)
	}

	choices := parsed.Choices
	if parsed.ChatResponse != nil && len(parsed.ChatResponse.Choices) > 0 {
		choices = parsed.ChatResponse.Choices
	}

	if len(choices) == 0 {
		return "", fmt.Errorf("oci-generative-ai: no choices in chat response")
	}

	var sb strings.Builder
	for _, choice := range choices {
		if choice.Message != nil && len(choice.Message.Content) > 0 {
			for _, c := range choice.Message.Content {
				sb.WriteString(c.Text)
			}
		} else if choice.Text != "" {
			sb.WriteString(choice.Text)
		}
	}
	return sb.String(), nil
}

type Client struct {
	Config     Config
	Signer     RequestSigner
	HTTPClient *http.Client
}

func (c *Client) Open(ctx context.Context, call lipapi.Call, model string) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("oci-generative-ai: tools are unsupported in this connector")
	}

	var messages []ChatMessage
	for _, m := range call.Messages {
		var role string
		switch m.Role {
		case lipapi.RoleUser:
			role = "USER"
		case lipapi.RoleAssistant:
			role = "ASSISTANT"
		case lipapi.RoleSystem:
			role = "SYSTEM"
		default:
			return nil, fmt.Errorf("oci-generative-ai: unsupported message role %q", m.Role)
		}

		var textBuilder strings.Builder
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("oci-generative-ai: unsupported part kind %q", p.Kind)
			}
			textBuilder.WriteString(p.Text)
		}
		messages = append(messages, ChatMessage{
			Role: role,
			Content: []ChatContent{
				{
					Type: "TEXT",
					Text: textBuilder.String(),
				},
			},
		})
	}

	isStreaming := call.Invocation.DeliveryMode != lipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != lipapi.TransportModeNonStreaming

	servingMode := map[string]string{
		"servingType": "ON_DEMAND",
		"modelId":     model,
	}
	if c.Config.DedicatedEndpointID != "" {
		servingMode = map[string]string{
			"servingType": "DEDICATED",
			"endpointId":  c.Config.DedicatedEndpointID,
		}
	}

	chatRequest := map[string]any{
		"apiFormat": "GENERIC",
		"messages":  messages,
		"isStream":  isStreaming,
	}
	if call.Options.MaxOutputTokens != nil {
		chatRequest["maxTokens"] = *call.Options.MaxOutputTokens
	}

	payload := map[string]any{
		"compartmentId": c.Config.CompartmentID,
		"servingMode":   servingMode,
		"chatRequest":   chatRequest,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: marshal payload: %w", err)
	}

	chatURL := c.Config.ChatEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if isStreaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Host = req.URL.Host

	if err := c.Signer.Sign(req); err != nil {
		return nil, fmt.Errorf("oci-generative-ai: sign request: %w", err)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: chat request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("oci-generative-ai: chat request status %d: %s", resp.StatusCode, string(body))
	}

	if isStreaming {
		return lipapi.CloseOnlyManagedStream{Stream: newSSEStream(ctx, resp.Body)}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: read response: %w", err)
	}

	genText, err := extractTextFromChatResponse(rawResp)
	if err != nil {
		return nil, err
	}

	events := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: genText},
		{Kind: lipapi.EventResponseFinished},
	}
	return lipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	mgmtURL := c.Config.ManagementEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mgmtURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: new list-models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Host = req.URL.Host

	if err := c.Signer.Sign(req); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: sign list-models request: %w", err)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: list models status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: read list models body: %w", err)
	}

	var parsed struct {
		ModelCollection *struct {
			Items []struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"items"`
		} `json:"modelCollection"`
		Items []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: unmarshal list models: %w", err)
	}

	rawItems := parsed.Items
	if parsed.ModelCollection != nil && len(parsed.ModelCollection.Items) > 0 {
		rawItems = parsed.ModelCollection.Items
	}

	out := make([]backendplugin.ModelDescriptor, 0, len(rawItems))
	for _, item := range rawItems {
		id := strings.TrimSpace(item.ID)
		name := strings.TrimSpace(item.DisplayName)
		if id == "" {
			id = name
		}
		if name == "" {
			name = id
		}
		if id == "" {
			continue
		}

		lowerID := strings.ToLower(id)
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerID, "embed") || strings.Contains(lowerID, "rerank") || strings.Contains(lowerID, "image") ||
			strings.Contains(lowerName, "embed") || strings.Contains(lowerName, "rerank") || strings.Contains(lowerName, "image") {
			continue
		}

		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + name,
			NativeModelID:    id,
			DisplayName:      name,
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
		return lipapi.Event{}, fmt.Errorf("oci-generative-ai: stream closed")
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
	ctx      context.Context
	body     io.ReadCloser
	scanner  *bufio.Scanner
	mu       sync.Mutex
	started  bool
	finished bool
	closed   bool
}

func newSSEStream(ctx context.Context, body io.ReadCloser) *sseStream {
	return &sseStream{
		ctx:     ctx,
		body:    body,
		scanner: bufio.NewScanner(body),
	}
}

func (s *sseStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return lipapi.Event{}, fmt.Errorf("oci-generative-ai: stream closed")
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

		text, err := extractTextFromChatResponse([]byte(data))
		if err == nil && text != "" {
			return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: text}, nil
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

func (s *sseStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
