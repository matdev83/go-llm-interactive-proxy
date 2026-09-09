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

const (
	defaultMaxBodyBytes int64 = 8 << 20
	defaultMaxSSEBytes  int64 = 32 << 20
)

type GenerateContentRequest struct {
	Contents          []VertexContent   `json:"contents"`
	SystemInstruction *VertexContent    `json:"systemInstruction,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
}

type VertexContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []VertexPart `json:"parts"`
}

type VertexPart struct {
	Text string `json:"text,omitempty"`
}

type GenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type GenerateContentResponse struct {
	Candidates    []VertexCandidate    `json:"candidates"`
	UsageMetadata *VertexUsageMetadata `json:"usageMetadata,omitempty"`
}

type VertexCandidate struct {
	Content      VertexContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index,omitempty"`
}

type VertexUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type Client struct {
	Config        Config
	TokenProvider TokenProvider
	HTTPClient    *http.Client
	MaxBodyBytes  int64
	MaxSSEBytes   int64
}

func (c *Client) maxBody() int64 {
	if c.MaxBodyBytes > 0 {
		return c.MaxBodyBytes
	}
	return defaultMaxBodyBytes
}

func (c *Client) maxSSE() int64 {
	if c.MaxSSEBytes > 0 {
		return c.MaxSSEBytes
	}
	return defaultMaxSSEBytes
}

func (c *Client) Open(ctx context.Context, call lipapi.Call, model string) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("vertex: model is required")
	}

	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("vertex: tools are not supported in this connector")
	}

	contents := make([]VertexContent, 0, len(call.Messages))
	for _, m := range call.Messages {
		var role string
		switch m.Role {
		case lipapi.RoleUser:
			role = "user"
		case lipapi.RoleAssistant:
			role = "model"
		default:
			return nil, fmt.Errorf("vertex: unsupported message role %q", m.Role)
		}
		var parts []VertexPart
		for _, p := range m.Parts {
			if p.Kind == lipapi.PartReasoning {
				return nil, fmt.Errorf("vertex: reasoning replay unsupported")
			}
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("vertex: unsupported part kind %q", p.Kind)
			}
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			parts = append(parts, VertexPart{Text: p.Text})
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("vertex: message has no text parts after trimming")
		}
		contents = append(contents, VertexContent{Role: role, Parts: parts})
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("vertex: no contents provided")
	}

	var sysInst *VertexContent
	if len(call.Instructions) > 0 {
		t := lipapi.JoinInstructionText(call.Instructions)
		if strings.TrimSpace(t) != "" {
			sysInst = &VertexContent{Parts: []VertexPart{{Text: t}}}
		}
	}

	var genConfig *GenerationConfig
	opts := call.Options
	if opts.Temperature != nil || opts.TopP != nil || opts.MaxOutputTokens != nil {
		genConfig = &GenerationConfig{
			Temperature:     opts.Temperature,
			TopP:            opts.TopP,
			MaxOutputTokens: opts.MaxOutputTokens,
		}
	}

	reqBody := GenerateContentRequest{
		Contents:          contents,
		SystemInstruction: sysInst,
		GenerationConfig:  genConfig,
	}
	rawJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vertex: marshal request body: %w", err)
	}

	stream := call.Invocation.DeliveryMode != lipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != lipapi.TransportModeNonStreaming

	endpoint := c.Config.ModelEndpoint(model, stream)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, fmt.Errorf("vertex: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.TokenProvider == nil {
		return nil, fmt.Errorf("vertex: token provider is required")
	}
	token, err := c.TokenProvider.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("vertex: get bearer token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vertex: request failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, c.maxBody()))
		return nil, fmt.Errorf("vertex: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	if stream {
		return lipapi.CloseOnlyManagedStream{Stream: newSSEStream(resp, c.maxSSE())}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody()))
	if err != nil {
		return nil, fmt.Errorf("vertex: read response: %w", err)
	}
	events, err := decodeNonStream(body)
	if err != nil {
		return nil, err
	}
	return lipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	endpoint := c.Config.InventoryEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: new inventory request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	if c.TokenProvider == nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: token provider is required")
	}
	token, err := c.TokenProvider.GetToken(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: get bearer token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: list models request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, c.maxBody()))
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody()))
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: read list models body: %w", err)
	}

	var listResp struct {
		PublisherModels []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"publisherModels"`
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("vertex: unmarshal list models: %w", err)
	}

	var rawModels []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	if len(listResp.PublisherModels) > 0 {
		rawModels = listResp.PublisherModels
	} else {
		rawModels = listResp.Models
	}

	out := make([]backendplugin.ModelDescriptor, 0, len(rawModels))
	for _, m := range rawModels {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		parts := strings.Split(name, "/")
		modelID := parts[len(parts)-1]
		if !isCodingCapableModel(modelID) {
			continue
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + modelID,
			NativeModelID:    modelID,
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
		return lipapi.Event{}, fmt.Errorf("vertex: stream closed")
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

func decodeNonStream(raw []byte) ([]lipapi.Event, error) {
	var resp GenerateContentResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("vertex: unmarshal non-stream response: %w", err)
	}

	var events []lipapi.Event
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseStarted})

	if resp.UsageMetadata != nil {
		events = append(events, usageEvent(resp.UsageMetadata))
	}

	msgStarted := false
	for _, cand := range resp.Candidates {
		for _, part := range cand.Content.Parts {
			if part.Text != "" {
				if !msgStarted {
					events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
					msgStarted = true
				}
				events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: part.Text})
			}
		}
	}

	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
	return events, nil
}

type sseStream struct {
	resp     *http.Response
	maxBytes int64
	sc       *bufio.Scanner
	read     int64
	pending  []lipapi.Event
	started  bool
	msgStart bool
	finished bool
	done     bool
	closed   bool
	mu       sync.Mutex
}

func newSSEStream(resp *http.Response, maxBytes int64) *sseStream {
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxBytes))
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &sseStream{
		resp:     resp,
		maxBytes: maxBytes,
		sc:       sc,
	}
}

func (s *sseStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return lipapi.Event{}, fmt.Errorf("vertex: stream closed")
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
				return lipapi.Event{}, fmt.Errorf("vertex: sse scan: %w", err)
			}
			s.done = true
			if s.started && !s.finished {
				s.finished = true
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
				continue
			}
			return lipapi.Event{}, io.EOF
		}

		line := s.sc.Text()
		s.read += int64(len(line) + 1)
		if s.read > s.maxBytes {
			return lipapi.Event{}, fmt.Errorf("vertex: sse exceeded %d bytes", s.maxBytes)
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
			if s.started && !s.finished {
				s.finished = true
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
			}
			continue
		}

		events, err := decodeSSEData([]byte(payload), &s.started, &s.msgStart)
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

func decodeSSEData(raw []byte, started, msgStart *bool) ([]lipapi.Event, error) {
	var resp GenerateContentResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("vertex: unmarshal SSE data: %w", err)
	}

	var events []lipapi.Event
	if !*started {
		events = append(events, lipapi.Event{Kind: lipapi.EventResponseStarted})
		*started = true
	}

	if resp.UsageMetadata != nil {
		events = append(events, usageEvent(resp.UsageMetadata))
	}

	for _, cand := range resp.Candidates {
		for _, part := range cand.Content.Parts {
			if part.Text != "" {
				if !*msgStart {
					events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
					*msgStart = true
				}
				events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: part.Text})
			}
		}
	}
	return events, nil
}

func usageEvent(u *VertexUsageMetadata) lipapi.Event {
	return lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  u.PromptTokenCount,
		OutputTokens: u.CandidatesTokenCount,
		TotalTokens:  u.TotalTokenCount,
		UsagePresence: lipapi.UsagePresence{
			InputTokens:  true,
			OutputTokens: true,
			TotalTokens:  true,
		},
	}
}
