package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

type Client struct {
	Config        Config
	TokenProvider TokenProvider
	HTTPClient    *http.Client
}

func (c *Client) Open(ctx context.Context, call lipapi.Call, targetModel string) (lipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, lipapi.ErrNilContext
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("watsonx: tools are not supported")
	}

	targetModel = strings.TrimSpace(targetModel)
	var deploymentID string
	var modelID string

	if dep, ok := strings.CutPrefix(targetModel, "deployment/"); ok {
		deploymentID = strings.TrimSpace(dep)
		if deploymentID == "" {
			return nil, fmt.Errorf("watsonx: deployment id is required")
		}
	} else {
		modelID = targetModel
		if modelID == "" {
			return nil, fmt.Errorf("watsonx: model id is required")
		}
	}

	isStreaming := call.Invocation.DeliveryMode != lipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != lipapi.TransportModeNonStreaming

	switch c.Config.InferenceAPI {
	case "chat":
		return c.openChat(ctx, call, modelID, deploymentID, isStreaming)
	case "generation":
		return c.openGeneration(ctx, call, modelID, deploymentID, isStreaming)
	default:
		return nil, fmt.Errorf("watsonx: unsupported inference_api %q", c.Config.InferenceAPI)
	}
}

type watsonxChatMessage struct {
	Role    string               `json:"role"`
	Content []watsonxContentPart `json:"content"`
}

type watsonxContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type watsonxChatRequest struct {
	ModelID     string               `json:"model_id,omitempty"`
	ProjectID   string               `json:"project_id,omitempty"`
	SpaceID     string               `json:"space_id,omitempty"`
	Messages    []watsonxChatMessage `json:"messages"`
	MaxTokens   *int                 `json:"max_tokens,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"`
}

type watsonxMessageContent struct {
	Text string
}

func (mc *watsonxMessageContent) UnmarshalJSON(data []byte) error {
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

type watsonxChatResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string                `json:"role"`
			Content watsonxMessageContent `json:"content"`
		} `json:"message"`
		Delta struct {
			Role    string                `json:"role"`
			Content watsonxMessageContent `json:"content"`
		} `json:"delta"`
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func extractTextFromChat(cr *watsonxChatResponse) string {
	if cr == nil || len(cr.Choices) == 0 {
		return ""
	}
	txt := cr.Choices[0].Message.Content.Text
	if txt == "" {
		txt = cr.Choices[0].Delta.Content.Text
	}
	if txt == "" {
		txt = cr.Choices[0].Text
	}
	return txt
}

func (c *Client) openChat(ctx context.Context, call lipapi.Call, modelID, deploymentID string, isStreaming bool) (lipapi.ManagedEventStream, error) {
	var reqURL string
	if isStreaming {
		if deploymentID != "" {
			reqURL = fmt.Sprintf("%s/ml/v1/deployments/%s/text/chat_stream?version=%s", c.Config.MLOrigin(), url.PathEscape(deploymentID), url.QueryEscape(c.Config.APIVersion))
		} else {
			reqURL = fmt.Sprintf("%s/ml/v1/text/chat_stream?version=%s", c.Config.MLOrigin(), url.QueryEscape(c.Config.APIVersion))
		}
	} else {
		if deploymentID != "" {
			reqURL = fmt.Sprintf("%s/ml/v1/deployments/%s/text/chat?version=%s", c.Config.MLOrigin(), url.PathEscape(deploymentID), url.QueryEscape(c.Config.APIVersion))
		} else {
			reqURL = fmt.Sprintf("%s/ml/v1/text/chat?version=%s", c.Config.MLOrigin(), url.QueryEscape(c.Config.APIVersion))
		}
	}

	chatReq := watsonxChatRequest{
		Temperature: call.Options.Temperature,
	}
	if deploymentID == "" {
		chatReq.ModelID = modelID
		if c.Config.ProjectID != "" {
			chatReq.ProjectID = c.Config.ProjectID
		} else {
			chatReq.SpaceID = c.Config.SpaceID
		}
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		mt := int(*call.Options.MaxOutputTokens)
		chatReq.MaxTokens = &mt
	}

	for _, msg := range call.Messages {
		var role string
		switch msg.Role {
		case lipapi.RoleUser:
			role = "user"
		case lipapi.RoleAssistant:
			role = "assistant"
		case lipapi.RoleSystem:
			role = "system"
		default:
			return nil, fmt.Errorf("watsonx: unsupported message role %q", msg.Role)
		}

		var parts []watsonxContentPart
		for _, p := range msg.Parts {
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("watsonx: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
			}
			parts = append(parts, watsonxContentPart{Type: "text", Text: p.Text})
		}
		chatReq.Messages = append(chatReq.Messages, watsonxChatMessage{
			Role:    role,
			Content: parts,
		})
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("watsonx: marshal chat request: %w", err)
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("watsonx: get token: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("watsonx: create request: %w", err)
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
		return nil, fmt.Errorf("watsonx: do chat request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("watsonx: chat request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	if isStreaming {
		return lipapi.CloseOnlyManagedStream{Stream: newSSEChatStream(ctx, resp.Body)}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("watsonx: read chat response: %w", err)
	}
	var cr watsonxChatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("watsonx: unmarshal chat response: %w", err)
	}

	text := extractTextFromChat(&cr)
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

type watsonxGenerationParameters struct {
	MaxNewTokens *int `json:"max_new_tokens,omitempty"`
}

type watsonxGenerationRequest struct {
	ModelID    string                       `json:"model_id,omitempty"`
	ProjectID  string                       `json:"project_id,omitempty"`
	SpaceID    string                       `json:"space_id,omitempty"`
	Input      string                       `json:"input"`
	Parameters *watsonxGenerationParameters `json:"parameters,omitempty"`
}

type watsonxGenerationResponse struct {
	Results []struct {
		GeneratedText string `json:"generated_text"`
		StopReason    string `json:"stop_reason"`
	} `json:"results"`
}

func (c *Client) openGeneration(ctx context.Context, call lipapi.Call, modelID, deploymentID string, isStreaming bool) (lipapi.ManagedEventStream, error) {
	var reqURL string
	if isStreaming {
		if deploymentID != "" {
			reqURL = fmt.Sprintf("%s/ml/v1/deployments/%s/text/generation_stream?version=%s", c.Config.MLOrigin(), url.PathEscape(deploymentID), url.QueryEscape(c.Config.APIVersion))
		} else {
			reqURL = fmt.Sprintf("%s/ml/v1/text/generation_stream?version=%s", c.Config.MLOrigin(), url.QueryEscape(c.Config.APIVersion))
		}
	} else {
		if deploymentID != "" {
			reqURL = fmt.Sprintf("%s/ml/v1/deployments/%s/text/generation?version=%s", c.Config.MLOrigin(), url.PathEscape(deploymentID), url.QueryEscape(c.Config.APIVersion))
		} else {
			reqURL = fmt.Sprintf("%s/ml/v1/text/generation?version=%s", c.Config.MLOrigin(), url.QueryEscape(c.Config.APIVersion))
		}
	}

	var sb strings.Builder
	for i, msg := range call.Messages {
		if i > 0 {
			sb.WriteString("\n")
		}
		for _, p := range msg.Parts {
			if p.Kind != lipapi.PartText {
				return nil, fmt.Errorf("watsonx: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
			}
			sb.WriteString(p.Text)
		}
	}

	genReq := watsonxGenerationRequest{
		Input: sb.String(),
	}
	if deploymentID == "" {
		genReq.ModelID = modelID
		if c.Config.ProjectID != "" {
			genReq.ProjectID = c.Config.ProjectID
		} else {
			genReq.SpaceID = c.Config.SpaceID
		}
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		mt := int(*call.Options.MaxOutputTokens)
		genReq.Parameters = &watsonxGenerationParameters{MaxNewTokens: &mt}
	}

	bodyBytes, err := json.Marshal(genReq)
	if err != nil {
		return nil, fmt.Errorf("watsonx: marshal generation request: %w", err)
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("watsonx: get token: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("watsonx: create request: %w", err)
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
		return nil, fmt.Errorf("watsonx: do generation request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("watsonx: generation request failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	if isStreaming {
		return lipapi.CloseOnlyManagedStream{Stream: newSSEGenerationStream(ctx, resp.Body)}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("watsonx: read generation response: %w", err)
	}
	var gr watsonxGenerationResponse
	if err := json.Unmarshal(respBody, &gr); err != nil {
		return nil, fmt.Errorf("watsonx: unmarshal generation response: %w", err)
	}

	var text string
	if len(gr.Results) > 0 {
		text = gr.Results[0].GeneratedText
	}

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

type foundationModelSpecsResponse struct {
	TotalCount int `json:"total_count"`
	Resources  []struct {
		ModelID   string `json:"model_id"`
		Label     string `json:"label"`
		Functions []struct {
			ID string `json:"id"`
		} `json:"functions"`
		Lifecycle []struct {
			ID string `json:"id"`
		} `json:"lifecycle"`
	} `json:"resources"`
}

type deploymentsResponse struct {
	TotalResults int `json:"total_results"`
	Resources    []struct {
		Metadata struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"metadata"`
		Entity struct {
			Name   string `json:"name"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"entity"`
	} `json:"resources"`
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: get token for list models: %w", err)
	}

	var models []backendplugin.ModelDescriptor

	// 1. Foundation models
	foundationFilter := "function_text_chat"
	if c.Config.InferenceAPI == "generation" {
		foundationFilter = "function_text_generation"
	}
	specsURL := fmt.Sprintf("%s/ml/v1/foundation_model_specs?version=%s&filters=%s",
		c.Config.MLOrigin(), url.QueryEscape(c.Config.APIVersion), url.QueryEscape(foundationFilter))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, specsURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: create foundation model specs request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: list foundation models request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: list foundation models failed (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: read foundation models response: %w", err)
	}

	var fResp foundationModelSpecsResponse
	if err := json.Unmarshal(body, &fResp); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: unmarshal foundation models response: %w", err)
	}

	for _, res := range fResp.Resources {
		mID := strings.TrimSpace(res.ModelID)
		if mID == "" {
			continue
		}

		// Check lifecycle: drop withdrawn/deprecated
		isWithdrawn := false
		for _, lc := range res.Lifecycle {
			id := strings.ToLower(strings.TrimSpace(lc.ID))
			if id == "withdrawn" || id == "deprecated" || id == "withdrawn_model" {
				isWithdrawn = true
				break
			}
		}
		if isWithdrawn {
			continue
		}

		// Check functions: drop non-chat/non-generation
		if len(res.Functions) > 0 {
			hasTextFunc := false
			for _, fn := range res.Functions {
				fnID := strings.ToLower(strings.TrimSpace(fn.ID))
				if fnID == "text_chat" || fnID == "text_generation" {
					hasTextFunc = true
					break
				}
			}
			if !hasTextFunc {
				continue
			}
		}

		// Conservative name heuristic backup: drop embed/rerank/image/vision
		lower := strings.ToLower(mID)
		if strings.Contains(lower, "embed") || strings.Contains(lower, "rerank") || strings.Contains(lower, "clip") || strings.Contains(lower, "withdrawn") {
			continue
		}

		displayName := strings.TrimSpace(res.Label)
		if displayName == "" {
			displayName = mID
		}

		models = append(models, backendplugin.ModelDescriptor{
			CanonicalModelID: "watsonx/" + mID,
			DisplayName:      displayName,
		})
	}

	// 2. Deployed models
	var deploymentsURL string
	if c.Config.ProjectID != "" {
		deploymentsURL = fmt.Sprintf("%s/ml/v4/deployments?version=2021-06-01&project_id=%s",
			c.Config.MLOrigin(), url.QueryEscape(c.Config.ProjectID))
	} else {
		deploymentsURL = fmt.Sprintf("%s/ml/v4/deployments?version=2021-06-01&space_id=%s",
			c.Config.MLOrigin(), url.QueryEscape(c.Config.SpaceID))
	}

	dReq, err := http.NewRequestWithContext(ctx, http.MethodGet, deploymentsURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: create deployments request: %w", err)
	}
	dReq.Header.Set("Authorization", "Bearer "+tok)
	dReq.Header.Set("Accept", "application/json")

	dResp, err := httpClient.Do(dReq)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: list deployments request: %w", err)
	}
	defer func() { _ = dResp.Body.Close() }()

	if dResp.StatusCode != http.StatusOK {
		dBody, _ := io.ReadAll(dResp.Body)
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: list deployments failed (status %d): %s", dResp.StatusCode, string(dBody))
	}

	dBody, err := io.ReadAll(dResp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: read deployments response: %w", err)
	}

	var depResp deploymentsResponse
	if err := json.Unmarshal(dBody, &depResp); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("watsonx: unmarshal deployments response: %w", err)
	}

	for _, d := range depResp.Resources {
		state := strings.ToLower(strings.TrimSpace(d.Entity.Status.State))
		if state != "ready" && state != "online" && state != "deployed" {
			continue
		}
		depID := strings.TrimSpace(d.Metadata.ID)
		if depID == "" {
			continue
		}
		name := strings.TrimSpace(d.Entity.Name)
		if name == "" {
			name = strings.TrimSpace(d.Metadata.Name)
		}
		if name == "" {
			name = "Deployment " + depID
		}
		models = append(models, backendplugin.ModelDescriptor{
			CanonicalModelID: "watsonx/deployment/" + depID,
			DisplayName:      name,
		})
	}

	if limit > 0 && len(models) > int(limit) {
		models = models[:limit]
	}

	return backendplugin.ListModelsResponse{
		Models:          models,
		InventorySource: FactoryKind,
		FetchedUnixMS:   c.nowUnixMS(),
	}, nil
}

func (c *Client) nowUnixMS() int64 {
	return 0 // default or current time
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
		return lipapi.Event{}, fmt.Errorf("watsonx: stream closed")
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
	msgStart bool
	finished bool
	closed   bool
	isGen    bool
}

func newSSEChatStream(ctx context.Context, body io.ReadCloser) *sseStream {
	return &sseStream{
		ctx:     ctx,
		body:    body,
		scanner: bufio.NewScanner(body),
	}
}

func newSSEGenerationStream(ctx context.Context, body io.ReadCloser) *sseStream {
	return &sseStream{
		ctx:     ctx,
		body:    body,
		scanner: bufio.NewScanner(body),
		isGen:   true,
	}
}

func (s *sseStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return lipapi.Event{}, fmt.Errorf("watsonx: stream closed")
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

		if s.isGen {
			var genResp watsonxGenerationResponse
			if err := json.Unmarshal([]byte(data), &genResp); err == nil && len(genResp.Results) > 0 {
				txt := genResp.Results[0].GeneratedText
				if txt != "" {
					if !s.msgStart {
						s.msgStart = true
					}
					return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: txt}, nil
				}
			}
		} else {
			var chunk watsonxChatResponse
			if err := json.Unmarshal([]byte(data), &chunk); err == nil {
				txt := extractTextFromChat(&chunk)
				if txt != "" {
					if !s.msgStart {
						s.msgStart = true
					}
					return lipapi.Event{Kind: lipapi.EventTextDelta, Delta: txt}, nil
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

func (s *sseStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}
