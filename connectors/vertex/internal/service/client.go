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
	Text       string            `json:"text,omitempty"`
	FileData   *VertexFileData   `json:"fileData,omitempty"`
	InlineData *VertexInlineData `json:"inlineData,omitempty"`
}

type VertexFileData struct {
	FileURI  string `json:"fileUri"`
	MIMEType string `json:"mimeType,omitempty"`
}

type VertexInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
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
	PromptTokenCount           int                        `json:"promptTokenCount"`
	CandidatesTokenCount       int                        `json:"candidatesTokenCount"`
	TotalTokenCount            int                        `json:"totalTokenCount"`
	CachedContentTokenCount    int                        `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount         int                        `json:"thoughtsTokenCount,omitempty"`
	ToolUsePromptTokenCount    int                        `json:"toolUsePromptTokenCount,omitempty"`
	PromptTokensDetails        []VertexModalityTokenCount `json:"promptTokensDetails,omitempty"`
	CandidatesTokensDetails    []VertexModalityTokenCount `json:"candidatesTokensDetails,omitempty"`
	CacheTokensDetails         []VertexModalityTokenCount `json:"cacheTokensDetails,omitempty"`
	ToolUsePromptTokensDetails []VertexModalityTokenCount `json:"toolUsePromptTokensDetails,omitempty"`
	TrafficType                string                     `json:"trafficType,omitempty"`
	ServiceTier                string                     `json:"serviceTier,omitempty"`

	inputTokenPresent     bool
	outputTokenPresent    bool
	totalTokenPresent     bool
	cacheTokenPresent     bool
	reasoningTokenPresent bool
	groundedToolPresent   bool
}

// UnmarshalJSON retains field presence that would otherwise be lost by the
// value-typed Vertex counters. Explicit zero is evidence; an omitted counter
// remains unavailable. The flags are internal and never serialized.
func (u *VertexUsageMetadata) UnmarshalJSON(data []byte) error {
	type plain VertexUsageMetadata
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*u = VertexUsageMetadata(decoded)
	_, u.inputTokenPresent = fields["promptTokenCount"]
	_, u.outputTokenPresent = fields["candidatesTokenCount"]
	_, u.totalTokenPresent = fields["totalTokenCount"]
	_, u.cacheTokenPresent = fields["cachedContentTokenCount"]
	_, u.reasoningTokenPresent = fields["thoughtsTokenCount"]
	_, u.groundedToolPresent = fields["toolUsePromptTokenCount"]
	return nil
}

type VertexModalityTokenCount struct {
	Modality   string `json:"modality,omitempty"`
	TokenCount int    `json:"tokenCount,omitempty"`
}

type Client struct {
	Config               Config
	TokenProvider        TokenProvider
	HTTPClient           *http.Client
	MaxBodyBytes         int64
	MaxSSEBytes          int64
	accountingEvidenceV1 bool
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
			switch p.Kind {
			case lipapi.PartText:
				if strings.TrimSpace(p.Text) == "" {
					continue
				}
				parts = append(parts, VertexPart{Text: p.Text})
			case lipapi.PartImageRef:
				if strings.TrimSpace(p.ImageRef) == "" {
					continue
				}
				parts = append(parts, VertexPart{FileData: &VertexFileData{FileURI: p.ImageRef, MIMEType: p.ImageMIME}})
			case lipapi.PartFileRef:
				if strings.TrimSpace(p.FileRef) == "" {
					continue
				}
				parts = append(parts, VertexPart{FileData: &VertexFileData{FileURI: p.FileRef, MIMEType: p.FileMIME}})
			default:
				return nil, fmt.Errorf("vertex: unsupported part kind %q", p.Kind)
			}
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
		managed := newSSEStream(resp, c.maxSSE())
		managed.SetEnabled(c.accountingEvidenceV1)
		return managed, nil
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
	managed := newSliceStream(events)
	managed.SetEnabled(c.accountingEvidenceV1)
	return managed, nil
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
	*backendplugin.UsageEvidenceBuffer
}

func newSliceStream(events []lipapi.Event) *sliceStream {
	s := &sliceStream{events: events, UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer()}
	for _, event := range events {
		if event.Kind == lipapi.EventUsageDelta {
			s.AddUsageEvent(event, "vertex.generate.usage:stream")
		}
	}
	return s
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
	return projectCanonicalUsageEvent(ev, s.UsageEvidenceBuffer), nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *sliceStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly, Err: s.Close()}
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
			appendVertexOutputPartEvents(&events, &msgStarted, part)
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
	*backendplugin.UsageEvidenceBuffer
}

func newSSEStream(resp *http.Response, maxBytes int64) *sseStream {
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxBytes))
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &sseStream{
		resp:                resp,
		maxBytes:            maxBytes,
		sc:                  sc,
		UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer(),
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
			return projectCanonicalUsageEvent(ev, s.UsageEvidenceBuffer), nil
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
		if s.UsageEvidenceBuffer != nil {
			for _, event := range events {
				if event.Kind == lipapi.EventUsageDelta {
					s.AddUsageEvent(event, "vertex.generate.usage:stream")
				}
			}
		}
		s.pending = append(s.pending, events...)
	}
}

// projectCanonicalUsageEvent leaves provider identity in the negotiated V1
// sideband while retaining the key for a legacy host that cannot consume it.
func projectCanonicalUsageEvent(ev lipapi.Event, bridge *backendplugin.UsageEvidenceBuffer) lipapi.Event {
	if ev.Kind == lipapi.EventUsageDelta && bridge != nil && bridge.AccountingEvidenceEnabled() {
		ev.Accounting.DedupeKey = ""
	}
	return ev
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

func (s *sseStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeTransport, Err: s.Close()}
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
			appendVertexOutputPartEvents(&events, msgStart, part)
		}
	}
	return events, nil
}

// appendVertexOutputPartEvents preserves provider output references while
// keeping raw inline media out of the canonical event stream. Vertex's
// fileData URI is a reference the customer-facing API can carry; inlineData
// is provider output bytes with no canonical output carrier and is therefore
// deliberately unavailable here rather than copied or priced locally.
func appendVertexOutputPartEvents(events *[]lipapi.Event, messageStarted *bool, part VertexPart) {
	if part.Text != "" {
		if !*messageStarted {
			*events = append(*events, lipapi.Event{Kind: lipapi.EventMessageStarted})
			*messageStarted = true
		}
		*events = append(*events, lipapi.Event{Kind: lipapi.EventTextDelta, Delta: part.Text})
	}
	if part.FileData == nil || strings.TrimSpace(part.FileData.FileURI) == "" {
		return
	}
	if !*messageStarted {
		*events = append(*events, lipapi.Event{Kind: lipapi.EventMessageStarted})
		*messageStarted = true
	}
	uri := strings.TrimSpace(part.FileData.FileURI)
	mime := strings.TrimSpace(part.FileData.MIMEType)
	if strings.HasPrefix(strings.ToLower(mime), "image/") {
		*events = append(*events, lipapi.Event{
			Kind: lipapi.EventAssistantImageRef, AssistantRef: uri, AssistantMIME: mime,
		})
		return
	}
	*events = append(*events, lipapi.Event{
		Kind: lipapi.EventAssistantFileRef, AssistantRef: uri, AssistantMIME: mime,
	})
}

func usageEvent(u *VertexUsageMetadata) lipapi.Event {
	if u == nil {
		return lipapi.Event{}
	}
	in, inputPresent := vertexCount(u.PromptTokenCount, u.inputTokenPresent)
	outBase, outputPresent := vertexCount(u.CandidatesTokenCount, u.outputTokenPresent)
	thoughts, thoughtsPresent := vertexCount(u.ThoughtsTokenCount, u.reasoningTokenPresent)
	cache, cachePresent := vertexCount(u.CachedContentTokenCount, u.cacheTokenPresent)
	total, totalPresent := vertexCount(u.TotalTokenCount, u.totalTokenPresent)
	if outputPresent && thoughtsPresent {
		// Both fields are independently provider-reported. Add only after
		// validating non-negative values; an overflow cannot be represented as
		// a trustworthy canonical int and therefore remains unavailable.
		if int64(outBase) > int64(^uint(0)>>1)-int64(thoughts) {
			outBase, outputPresent = 0, false
		} else {
			outBase += thoughts
		}
	} else if !outputPresent {
		outBase, outputPresent = thoughts, thoughtsPresent
	}
	return lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     in,
		OutputTokens:    outBase,
		TotalTokens:     total,
		CacheReadTokens: cache,
		ReasoningTokens: thoughts,
		UsagePresence: lipapi.UsagePresence{
			InputTokens:     inputPresent,
			OutputTokens:    outputPresent,
			CacheReadTokens: cachePresent,
			ReasoningTokens: thoughtsPresent,
			TotalTokens:     totalPresent,
		},
		RawUsageJSON: vertexUsageRawJSON(u),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "vertex.generate.usage:stream",
			ServiceContext: vertexServiceContext(u),
		},
	}
}

func vertexServiceContext(u *VertexUsageMetadata) string {
	if u == nil {
		return ""
	}
	if traffic := strings.TrimSpace(u.TrafficType); traffic != "" {
		return traffic
	}
	return strings.TrimSpace(u.ServiceTier)
}

func vertexCount(value int, explicit bool) (int, bool) {
	if value < 0 {
		return 0, false
	}
	return value, explicit || value != 0
}
