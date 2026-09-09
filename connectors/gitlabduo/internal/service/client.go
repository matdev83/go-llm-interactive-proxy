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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// Client handles GitLab Duo communication for a single configured generation.
type Client struct {
	Config              Config
	TokenProvider       TokenProvider
	DirectAccessManager DirectAccessManager
	HTTPClient          *http.Client

	mu             sync.Mutex
	cachedWorkflow []backendplugin.ModelDescriptor
	discoveryDone  bool
}

func (c *Client) getHTTPClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// StaticModels returns the agentic chat models documented at the pin.
func StaticModels() []backendplugin.ModelDescriptor {
	return []backendplugin.ModelDescriptor{
		{
			CanonicalModelID: FactoryKind + "/" + ModelHaiku45,
			NativeModelID:    ModelHaiku45,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
		{
			CanonicalModelID: FactoryKind + "/" + ModelSonnet45,
			NativeModelID:    ModelSonnet45,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
		{
			CanonicalModelID: FactoryKind + "/" + ModelOpus45,
			NativeModelID:    ModelOpus45,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
	}
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLModelRef struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type graphQLResponse struct {
	Data *struct {
		AiChatAvailableModels *struct {
			DefaultModel     *graphQLModelRef  `json:"defaultModel"`
			SelectableModels []graphQLModelRef `json:"selectableModels"`
			PinnedModel      *graphQLModelRef  `json:"pinnedModel"`
		} `json:"aiChatAvailableModels"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

const aiChatAvailableModelsQuery = `query aiChatAvailableModels($rootNamespaceId: GroupID!) {
  aiChatAvailableModels(rootNamespaceId: $rootNamespaceId) {
    defaultModel { name ref }
    selectableModels { name ref }
    pinnedModel { name ref }
  }
}`

// ListModels returns static agentic chat models and dynamically discovered models when namespace is configured.
// Discovery is cached in-memory for the lifetime of this Client instance (one Configure generation).
func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	staticList := StaticModels()
	if c.discoveryDone {
		combined := append([]backendplugin.ModelDescriptor(nil), staticList...)
		combined = append(combined, c.cachedWorkflow...)
		if limit > 0 && uint32(len(combined)) > limit {
			combined = combined[:limit]
		}
		return backendplugin.ListModelsResponse{
			Models:          combined,
			InventorySource: FactoryKind,
			FetchedUnixMS:   time.Now().UnixMilli(),
		}, nil
	}

	discovered, err := c.discoverModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}

	c.cachedWorkflow = discovered
	c.discoveryDone = true

	combined := append([]backendplugin.ModelDescriptor(nil), staticList...)
	combined = append(combined, discovered...)
	if limit > 0 && uint32(len(combined)) > limit {
		combined = combined[:limit]
	}
	return backendplugin.ListModelsResponse{
		Models:          combined,
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

func (c *Client) resolveRootNamespaceID(ctx context.Context, token string) (string, bool, error) {
	if c.Config.RootNamespaceID != "" {
		id := c.Config.RootNamespaceID
		if !strings.HasPrefix(id, "gid://") {
			return fmt.Sprintf("gid://gitlab/Group/%s", id), true, nil
		}
		return id, true, nil
	}

	if c.Config.ProjectPath != "" {
		projURL := fmt.Sprintf("%s/api/v4/projects/%s", c.Config.GetInstanceURL(), url.QueryEscape(c.Config.ProjectPath))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, projURL, nil)
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", DefaultUserAgent)
		resp, err := c.getHTTPClient().Do(req)
		if err != nil {
			return "", false, fmt.Errorf("gitlab-duo: project lookup failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return "", false, fmt.Errorf("gitlab-duo: project lookup failed with status %d: %s", resp.StatusCode, string(body))
		}
		var proj struct {
			Namespace struct {
				ID int64 `json:"id"`
			} `json:"namespace"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&proj); err != nil {
			return "", false, fmt.Errorf("gitlab-duo: decode project response: %w", err)
		}
		if proj.Namespace.ID == 0 {
			return "", false, fmt.Errorf("gitlab-duo: project %s has no namespace id", c.Config.ProjectPath)
		}
		return fmt.Sprintf("gid://gitlab/Group/%d", proj.Namespace.ID), true, nil
	}

	// Neither root_namespace_id nor project_path configured.
	// Do not probe namespaces, do not invent Group/1. Return false to indicate no GraphQL discovery.
	return "", false, nil
}

func (c *Client) discoverModels(ctx context.Context) ([]backendplugin.ModelDescriptor, error) {
	if c.TokenProvider == nil {
		return nil, errors.New("gitlab-duo: token provider required for discovery")
	}

	token, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: auth token for discovery: %w", err)
	}

	rootNs, hasNs, err := c.resolveRootNamespaceID(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: resolve root namespace: %w", err)
	}
	if !hasNs {
		// Neither root_namespace_id nor project_path configured.
		// Skip GraphQL call completely.
		return nil, nil
	}

	endpoint := fmt.Sprintf("%s/api/graphql", c.Config.GetInstanceURL())
	gqlReq := graphQLRequest{
		Query: aiChatAvailableModelsQuery,
		Variables: map[string]any{
			"rootNamespaceId": rootNs,
		},
	}
	reqBytes, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("gitlab-duo: discovery failed with status %d: %s", resp.StatusCode, string(body))
	}

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("gitlab-duo: decode discovery response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("gitlab-duo: discovery graphql error: %s", gqlResp.Errors[0].Message)
	}

	if gqlResp.Data == nil || gqlResp.Data.AiChatAvailableModels == nil {
		return nil, nil
	}

	aim := gqlResp.Data.AiChatAvailableModels
	var candidates []graphQLModelRef
	if aim.PinnedModel != nil && aim.PinnedModel.Ref != "" {
		candidates = append(candidates, *aim.PinnedModel)
	}
	for _, m := range aim.SelectableModels {
		if m.Ref != "" {
			candidates = append(candidates, m)
		}
	}
	if aim.DefaultModel != nil && aim.DefaultModel.Ref != "" {
		candidates = append(candidates, *aim.DefaultModel)
	}

	seen := make(map[string]struct{})
	var result []backendplugin.ModelDescriptor
	for _, m := range candidates {
		ref := strings.TrimSpace(m.Ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}

		dispName := m.Name
		if dispName == "" {
			dispName = "GitLab Duo (" + ref + ")"
		}

		result = append(result, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + ref,
			NativeModelID:    ref,
			DisplayName:      dispName,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
	}

	return result, nil
}

func (c *Client) resolveGatewayBase(da DirectAccessToken) string {
	if c.Config.AIGatewayURL != "" {
		return strings.TrimRight(c.Config.AIGatewayURL, "/")
	}
	if da.BaseURL != "" {
		return strings.TrimRight(da.BaseURL, "/")
	}
	return DefaultAIGatewayURL
}

func mapToBackendModel(modelID string) (backendModel string, isOpenAI bool) {
	m := strings.TrimSpace(modelID)
	if after, ok := strings.CutPrefix(m, FactoryKind+"/"); ok {
		m = after
	}

	switch m {
	case ModelHaiku45:
		return BackendClaudeHaiku45, false
	case ModelSonnet45:
		return BackendClaudeSonnet45, false
	case ModelOpus45:
		return BackendClaudeOpus45, false
	default:
		lower := strings.ToLower(m)
		if strings.Contains(lower, "gpt") || strings.Contains(lower, "openai") {
			return m, true
		}
		return m, false
	}
}

// Execute opens an inference stream towards GitLab's AI Gateway.
func (c *Client) Execute(ctx context.Context, inv backendplugin.Invocation, call lipapi.Call, requestedModel string) (lipapi.ManagedEventStream, error) {
	// Hard negatives: reject ACP requests or non-inference repository tool calls.
	if inv.Operation == "agent_control" || strings.Contains(strings.ToLower(inv.Operation), "acp") {
		return nil, errors.New("gitlab-duo: ACP is not supported; inference connector only")
	}
	for _, t := range call.Tools {
		name := strings.ToLower(t.Name)
		if strings.Contains(name, "gitlab_mr") ||
			strings.Contains(name, "gitlab_issue") ||
			strings.Contains(name, "gitlab_pipeline") ||
			strings.Contains(name, "repository") {
			return nil, fmt.Errorf("gitlab-duo: repository tools are out of scope; inference connector only: %s", t.Name)
		}
	}
	for _, t := range inv.Tools {
		name := strings.ToLower(t.Name)
		if strings.Contains(name, "gitlab_mr") ||
			strings.Contains(name, "gitlab_issue") ||
			strings.Contains(name, "gitlab_pipeline") ||
			strings.Contains(name, "repository") {
			return nil, fmt.Errorf("gitlab-duo: repository tools are out of scope; inference connector only: %s", t.Name)
		}
	}

	if c.DirectAccessManager == nil {
		return nil, errors.New("gitlab-duo: direct access manager not configured")
	}

	da, err := c.DirectAccessManager.GetDirectAccessToken(ctx, false)
	if err != nil {
		return nil, err
	}

	modelID := requestedModel
	if modelID == "" {
		modelID = c.Config.DefaultModel()
	}

	backendModel, isOpenAI := mapToBackendModel(modelID)
	if isOpenAI {
		return c.executeOpenAI(ctx, da, backendModel, inv, call)
	}

	return c.executeAnthropic(ctx, da, backendModel, inv, call)
}

type messageItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func extractMessages(call lipapi.Call, inv backendplugin.Invocation) ([]messageItem, string) {
	var items []messageItem
	var systemPrompt string

	if len(call.Messages) > 0 {
		for _, m := range call.Messages {
			if m.Role == lipapi.RoleSystem {
				for _, p := range m.Parts {
					if p.Kind == lipapi.PartText {
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
				if p.Kind == lipapi.PartText {
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

func (c *Client) executeAnthropic(ctx context.Context, da DirectAccessToken, backendModel string, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
	gatewayBase := c.resolveGatewayBase(da)
	endpoint := fmt.Sprintf("%s/ai/v1/proxy/anthropic/v1/messages", gatewayBase)

	isStreaming := inv.DeliveryMode != string(lipapi.DeliveryModeNonStreaming)

	msgs, systemPrompt := extractMessages(call, inv)

	reqPayload := map[string]any{
		"model":      backendModel,
		"messages":   msgs,
		"max_tokens": 8192,
		"stream":     isStreaming,
	}
	if systemPrompt != "" {
		reqPayload["system"] = systemPrompt
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+da.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("anthropic-beta", "context-1m-2025-08-07")

	for k, v := range da.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: messages request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		// Try refreshing direct access token once
		da2, rerr := c.DirectAccessManager.GetDirectAccessToken(ctx, true)
		if rerr != nil {
			return nil, fmt.Errorf("gitlab-duo: token refresh after 401 failed: %w", rerr)
		}
		endpoint2 := fmt.Sprintf("%s/ai/v1/proxy/anthropic/v1/messages", c.resolveGatewayBase(da2))
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint2, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+da2.Token)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", DefaultUserAgent)
		req2.Header.Set("anthropic-beta", "context-1m-2025-08-07")
		for k, v := range da2.Headers {
			req2.Header.Set(k, v)
		}
		resp, err = c.getHTTPClient().Do(req2)
		if err != nil {
			return nil, fmt.Errorf("gitlab-duo: retry messages request: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("gitlab-duo: messages error (status %d): %s", resp.StatusCode, string(body))
	}

	if isStreaming {
		return newAnthropicManagedSSEStream(resp), nil
	}

	return newUnaryAnthropicStream(resp)
}

func (c *Client) executeOpenAI(ctx context.Context, da DirectAccessToken, backendModel string, inv backendplugin.Invocation, call lipapi.Call) (lipapi.ManagedEventStream, error) {
	gatewayBase := c.resolveGatewayBase(da)
	endpoint := fmt.Sprintf("%s/ai/v1/proxy/openai/v1/chat/completions", gatewayBase)

	isStreaming := inv.DeliveryMode != string(lipapi.DeliveryModeNonStreaming)

	msgs, systemPrompt := extractMessages(call, inv)
	if systemPrompt != "" {
		msgs = append([]messageItem{{Role: "system", Content: systemPrompt}}, msgs...)
	}

	reqPayload := map[string]any{
		"model":      backendModel,
		"messages":   msgs,
		"max_tokens": 4096,
		"stream":     isStreaming,
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+da.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	for k, v := range da.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: openai proxy request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		// Try refreshing direct access token once
		da2, rerr := c.DirectAccessManager.GetDirectAccessToken(ctx, true)
		if rerr != nil {
			return nil, fmt.Errorf("gitlab-duo: token refresh after 401 failed: %w", rerr)
		}
		endpoint2 := fmt.Sprintf("%s/ai/v1/proxy/openai/v1/chat/completions", c.resolveGatewayBase(da2))
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint2, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+da2.Token)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", DefaultUserAgent)
		for k, v := range da2.Headers {
			req2.Header.Set(k, v)
		}
		resp, err = c.getHTTPClient().Do(req2)
		if err != nil {
			return nil, fmt.Errorf("gitlab-duo: retry openai proxy request: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("gitlab-duo: openai proxy error (status %d): %s", resp.StatusCode, string(body))
	}

	if isStreaming {
		return newOpenAIManagedSSEStream(resp), nil
	}

	return newUnaryOpenAIStream(resp)
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

// Unary Anthropic Response handling
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
		return nil, fmt.Errorf("gitlab-duo: decode unary anthropic response: %w", err)
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

	return &memoryEventStream{events: events}, nil
}

// OpenAI SSE stream processing
type openAIManagedSSEStream struct {
	resp     *http.Response
	sc       *bufio.Scanner
	mu       sync.Mutex
	closed   bool
	started  bool
	msgStart bool
	finished bool
	done     bool
	pending  []lipapi.Event
}

func newOpenAIManagedSSEStream(resp *http.Response) *openAIManagedSSEStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &openAIManagedSSEStream{resp: resp, sc: sc}
}

func (s *openAIManagedSSEStream) Recv(ctx context.Context) (lipapi.Event, error) {
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
			if !s.finished && s.started {
				s.finished = true
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return ev, nil
			}
			return lipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			s.done = true
			if !s.finished && s.started {
				s.finished = true
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished})
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return ev, nil
			}
			continue
		}
		var p struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			continue
		}
		if !s.started {
			s.started = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseStarted})
		}
		if !s.msgStart {
			s.msgStart = true
			s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventMessageStarted})
		}
		for _, ch := range p.Choices {
			if ch.Delta.Content != "" {
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: ch.Delta.Content})
			}
			if ch.FinishReason != "" && !s.finished {
				s.finished = true
				s.pending = append(s.pending, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: ch.FinishReason})
			}
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
	}
}

func (s *openAIManagedSSEStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	err := s.Close()
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly, Err: err}
}

func (s *openAIManagedSSEStream) Close() error {
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

// Unary OpenAI Response handling
func newUnaryOpenAIStream(resp *http.Response) (lipapi.ManagedEventStream, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("gitlab-duo: decode unary openai response: %w", err)
	}

	var events []lipapi.Event
	events = append(events, lipapi.Event{Kind: lipapi.EventResponseStarted})
	events = append(events, lipapi.Event{Kind: lipapi.EventMessageStarted})
	if res.Usage.PromptTokens > 0 {
		events = append(events, lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: res.Usage.PromptTokens})
	}
	if len(res.Choices) > 0 {
		ch := res.Choices[0]
		if ch.Message.Content != "" {
			events = append(events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: ch.Message.Content})
		}
		if res.Usage.CompletionTokens > 0 {
			events = append(events, lipapi.Event{Kind: lipapi.EventUsageDelta, OutputTokens: res.Usage.CompletionTokens})
		}
		events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished, FinishReason: ch.FinishReason})
	} else {
		events = append(events, lipapi.Event{Kind: lipapi.EventResponseFinished})
	}

	return &memoryEventStream{events: events}, nil
}

type memoryEventStream struct {
	events []lipapi.Event
	idx    int
}

func (m *memoryEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	if m.idx >= len(m.events) {
		return lipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *memoryEventStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (m *memoryEventStream) Close() error {
	m.idx = len(m.events)
	return nil
}
