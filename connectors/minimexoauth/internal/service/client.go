package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/oauthcred"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// Client handles MiniMax OAuth communication and Anthropic Messages inference.
type Client struct {
	Config        Config
	TokenProvider TokenProvider
	OAuthSession  *oauthcred.Session
	HTTPClient    *http.Client
}

func (c *Client) getHTTPClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// StaticModels returns the authoritative initial models for MiniMax OAuth.
func StaticModels() []backendplugin.ModelDescriptor {
	return []backendplugin.ModelDescriptor{
		{
			CanonicalModelID: FactoryKind + "/" + ModelM27,
			NativeModelID:    ModelM27,
			DisplayName:      "MiniMax M2.7",
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
		{
			CanonicalModelID: FactoryKind + "/" + ModelM27Highspeed,
			NativeModelID:    ModelM27Highspeed,
			DisplayName:      "MiniMax M2.7 Highspeed",
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
	}
}

// ListModels queries the models inventory API if configured and token is present; fails closed on non-200.
func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	staticList := StaticModels()

	if c.TokenProvider == nil {
		return backendplugin.ListModelsResponse{Models: staticList}, nil
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		// If token cannot be retrieved, fail closed
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: retrieve token for models inventory: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/models", strings.TrimRight(c.Config.InferenceBaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: list models request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errText := readBoundedError(resp, ErrorBodyLimit)
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: list models API failed (status %d): %s", resp.StatusCode, errText)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: decode list models JSON: %w", err)
	}

	var candidates []string
	for _, m := range payload.Data {
		if m.ID != "" {
			candidates = append(candidates, m.ID)
		}
	}
	for _, m := range payload.Models {
		if m.ID != "" {
			candidates = append(candidates, m.ID)
		}
	}

	if len(candidates) == 0 {
		return backendplugin.ListModelsResponse{Models: staticList}, nil
	}

	seen := make(map[string]struct{})
	var result []backendplugin.ModelDescriptor
	for _, cID := range candidates {
		native := cID
		if after, ok := strings.CutPrefix(cID, FactoryKind+"/"); ok {
			native = after
		}
		canonical := FactoryKind + "/" + native
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			result = append(result, backendplugin.ModelDescriptor{
				CanonicalModelID: canonical,
				NativeModelID:    native,
				DisplayName:      "MiniMax (" + native + ")",
				FactoryKind:      FactoryKind,
				Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
			})
		}
	}

	// Ensure static models are always present in the returned list
	for _, s := range staticList {
		if _, ok := seen[s.CanonicalModelID]; !ok {
			seen[s.CanonicalModelID] = struct{}{}
			result = append(result, s)
		}
	}

	return backendplugin.ListModelsResponse{Models: result}, nil
}

type messageItem struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// Execute opens an Anthropic Messages inference stream towards MiniMax.
func (c *Client) Execute(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call, requestedModel string) (lipapi.ManagedEventStream, error) {
	// Hard negatives:
	if inv.Operation == string(lipapi.OperationOpenAIResponses) ||
		inv.Operation == string(lipapi.OperationOpenResponsesCreate) {
		return nil, fmt.Errorf("minimax-oauth: responses operations are not supported; anthropic messages transport only")
	}
	if inv.Operation == "agent_control" || strings.Contains(strings.ToLower(inv.Operation), "acp") {
		return nil, errors.New("minimax-oauth: ACP is not supported; inference connector only")
	}

	model := strings.TrimSpace(requestedModel)
	if model == "" || model == FactoryKind {
		model = ModelM27
	}
	if after, ok := strings.CutPrefix(model, FactoryKind+"/"); ok {
		model = after
	}

	isStreaming := inv.DeliveryMode != string(lipapi.DeliveryModeNonStreaming)

	msgs, systemPrompt := extractAnthropicMessages(call, inv)

	reqPayload := map[string]any{
		"model":      model,
		"messages":   msgs,
		"max_tokens": 8192,
		"stream":     isStreaming,
	}
	if systemPrompt != "" {
		reqPayload["system"] = systemPrompt
	}

	// Tools support (standard Anthropic wire format)
	tools := extractTools(call, inv)
	if len(tools) > 0 {
		reqPayload["tools"] = tools
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: marshal payload: %w", err)
	}

	if c.TokenProvider == nil {
		return nil, errors.New("minimax-oauth: token provider not configured")
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: acquire token: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/messages", strings.TrimRight(c.Config.InferenceBaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("anthropic-version", "2023-06-01")
	if isStreaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: inference request: %w", err)
	}

	// Transient 401 retry once with refreshed token
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		newTok, rerr := c.TokenProvider.ForceRefresh(ctx)
		if rerr != nil {
			return nil, fmt.Errorf("minimax-oauth: token refresh after 401 failed: %w", rerr)
		}

		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+newTok)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", DefaultUserAgent)
		req2.Header.Set("anthropic-version", "2023-06-01")
		if isStreaming {
			req2.Header.Set("Accept", "text/event-stream")
		} else {
			req2.Header.Set("Accept", "application/json")
		}

		resp, err = c.getHTTPClient().Do(req2)
		if err != nil {
			return nil, fmt.Errorf("minimax-oauth: retry inference request: %w", err)
		}
	}

	// Entitlement 403 fails closed immediately without refresh
	if resp.StatusCode == http.StatusForbidden {
		defer func() { _ = resp.Body.Close() }()
		errText := readBoundedError(resp, ErrorBodyLimit)
		return nil, fmt.Errorf("minimax-oauth: entitlement error (status 403): %s", errText)
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		errText := readBoundedError(resp, ErrorBodyLimit)
		return nil, fmt.Errorf("minimax-oauth: inference failed (status %d): %s", resp.StatusCode, errText)
	}

	if isStreaming {
		return newAnthropicManagedSSEStream(resp), nil
	}

	return newUnaryAnthropicStream(resp)
}

func extractAnthropicMessages(call lipapi.Call, inv backendplugin.Invocation) ([]messageItem, string) {
	var items []messageItem
	var systemPrompt string

	if len(call.Messages) > 0 {
		for _, m := range call.Messages {
			if m.Role == lipapi.RoleSystem {
				for _, p := range m.Parts {
					if p.Kind == lipapi.PartText && p.Text != "" {
						if systemPrompt != "" {
							systemPrompt += "\n\n"
						}
						systemPrompt += p.Text
					}
				}
				continue
			}
			role := "user"
			if m.Role == lipapi.RoleAssistant {
				role = "assistant"
			}
			var sb strings.Builder
			for _, p := range m.Parts {
				if p.Kind == lipapi.PartText && p.Text != "" {
					sb.WriteString(p.Text)
				}
			}
			items = append(items, messageItem{Role: role, Content: sb.String()})
		}
	} else {
		for _, m := range inv.Messages {
			if m.Role == backendplugin.RoleSystem {
				for _, p := range m.Parts {
					if p.Kind == backendplugin.PartKindText && p.Text != nil {
						if systemPrompt != "" {
							systemPrompt += "\n\n"
						}
						systemPrompt += *p.Text
					}
				}
				continue
			}
			role := "user"
			if m.Role == backendplugin.RoleAssistant {
				role = "assistant"
			}
			var sb strings.Builder
			for _, p := range m.Parts {
				if p.Kind == backendplugin.PartKindText && p.Text != nil {
					sb.WriteString(*p.Text)
				}
			}
			items = append(items, messageItem{Role: role, Content: sb.String()})
		}
	}

	if len(items) == 0 {
		items = append(items, messageItem{Role: "user", Content: "Hello"})
	}
	return items, systemPrompt
}

func extractTools(call lipapi.Call, inv backendplugin.Invocation) []map[string]any {
	var tools []map[string]any
	for _, t := range call.Tools {
		if t.Name == "" {
			continue
		}
		tools = append(tools, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	if len(tools) == 0 {
		for _, t := range inv.Tools {
			if t.Name == "" {
				continue
			}
			var schema any
			if b := t.ParametersJSON.Bytes(); len(b) > 0 {
				_ = json.Unmarshal(b, &schema)
			}
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": schema,
			})
		}
	}
	return tools
}

// anthropicManagedSSEStream processes SSE chunks from Anthropic proxy.
type anthropicManagedSSEStream struct {
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

func newAnthropicManagedSSEStream(resp *http.Response) *anthropicManagedSSEStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &anthropicManagedSSEStream{resp: resp, sc: sc, toolCalls: make(map[int]string)}
}

type anthropicSSEPayload struct {
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

func (s *anthropicManagedSSEStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
		var p anthropicSSEPayload
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

func (s *anthropicManagedSSEStream) ensureStarted() {
	if !s.started {
		s.started = true
		s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
	}
	if !s.msgStart {
		s.msgStart = true
		s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventMessageStarted})
	}
}

func (s *anthropicManagedSSEStream) emitFinishIfStarted(reason string) {
	if s.finished || !s.started {
		return
	}
	s.finished = true
	s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: reason})
}

func (s *anthropicManagedSSEStream) handlePayload(p anthropicSSEPayload) error {
	switch p.Type {
	case "message_start":
		if !s.started {
			s.started = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		u := p.Message.Usage
		if u.InputTokens > 0 || u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
			s.pending = append(s.pending, lipapi.Event{
				Kind:             lipapi.EventUsageDelta,
				InputTokens:      u.InputTokens,
				CacheReadTokens:  u.CacheReadInputTokens,
				CacheWriteTokens: u.CacheCreationInputTokens,
			})
		}
	case "content_block_start":
		s.ensureStarted()
		if p.ContentBlock.Type == "tool_use" {
			toolID := p.ContentBlock.ID
			s.toolCalls[p.Index] = toolID
			s.pending = append(s.pending, lipapi.Event{
				Kind:       lipapi.EventToolCallStarted,
				ToolCallID: toolID,
				ToolName:   p.ContentBlock.Name,
			})
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
				s.pending = append(s.pending, lipapi.Event{
					Kind:       lipapi.EventToolCallArgsDelta,
					ToolCallID: s.toolCalls[p.Index],
					Delta:      p.Delta.PartialJSON,
				})
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

func (s *anthropicManagedSSEStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	err := s.Close()
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly, Err: err}
}

func (s *anthropicManagedSSEStream) Close() error {
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

// newUnaryAnthropicStream converts a unary JSON response into a managed event stream.
func newUnaryAnthropicStream(resp *http.Response) (lipapi.ManagedEventStream, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("minimax-oauth: decode unary anthropic response: %w", err)
	}

	var events []lipapi.Event
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseStarted})
	events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
	if res.Usage.InputTokens > 0 {
		events = append(events, lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: res.Usage.InputTokens})
	}
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: c.Text})
		}
	}
	if res.Usage.OutputTokens > 0 {
		events = append(events, lipapi.Event{Kind: lipapi.EventUsageDelta, OutputTokens: res.Usage.OutputTokens})
	}
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: res.StopReason})

	return &unaryAnthropicStream{events: events}, nil
}

type unaryAnthropicStream struct {
	events []lipapi.Event
	idx    int
	mu     sync.Mutex
	closed bool
}

func (u *unaryAnthropicStream) Recv(ctx context.Context) (lipapi.Event, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return lipapi.Event{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	if u.idx >= len(u.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := u.events[u.idx]
	u.idx++
	return ev, nil
}

func (u *unaryAnthropicStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	_ = u.Close()
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (u *unaryAnthropicStream) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	return nil
}
