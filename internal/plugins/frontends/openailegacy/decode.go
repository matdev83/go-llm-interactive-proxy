package openailegacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonutil"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openaiwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Extension keys for round-trip metadata on the canonical call.
const (
	extModelJSONKey      = "openailegacy.model"
	extStreamOptsJSONKey = "openailegacy.stream_options"
)

// DecodeOptions configures decoding of a Chat Completions request.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:gpt-4o-mini"); usually from X-LIP-Route header.
	RouteSelector string
}

// DecodedChat is the result of decoding POST /v1/chat/completions JSON.
type DecodedChat struct {
	Call   *lipapi.Call
	Stream bool
	Model  string
}

type wireCreate struct {
	Model             string            `json:"model"`
	Stream            bool              `json:"stream"`
	Messages          []json.RawMessage `json:"messages"`
	Tools             json.RawMessage   `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	Temperature       *float64          `json:"temperature"`
	TopP              *float64          `json:"top_p"`
	MaxTokens         *int              `json:"max_tokens"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	StreamOptions     json.RawMessage   `json:"stream_options"`
}

// DecodeChatRequest maps a Chat Completions JSON body into a canonical call.
func DecodeChatRequest(body []byte, opts DecodeOptions) (*DecodedChat, error) {
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("openailegacy: route selector is required")
	}
	var w wireCreate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("openailegacy: invalid json: %w", err)
	}
	model := strings.TrimSpace(w.Model)
	if model == "" {
		return nil, errors.New("openailegacy: model is required")
	}
	if len(w.Messages) == 0 {
		return nil, errors.New("openailegacy: messages is required")
	}

	msgs, err := parseMessages(w.Messages)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: messages: %w", err)
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: tool_choice: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if jsonutil.IsPresentNonNullJSON(w.StreamOptions) {
		ext[extStreamOptsJSONKey] = w.StreamOptions
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: sel},
		Messages:   msgs,
		Tools:      tools,
		ToolChoice: toolChoice,
		Extensions: ext,
		Options: lipapi.GenerationOptions{
			Temperature:       w.Temperature,
			TopP:              w.TopP,
			MaxOutputTokens:   w.MaxTokens,
			ParallelToolCalls: w.ParallelToolCalls,
		},
	}
	return &DecodedChat{Call: call, Stream: w.Stream, Model: model}, nil
}

func parseMessages(raw []json.RawMessage) ([]lipapi.Message, error) {
	out := make([]lipapi.Message, 0, len(raw))
	for i, it := range raw {
		m, err := parseMessage(it)
		if err != nil {
			return nil, fmt.Errorf("openailegacy: messages[%d]: %w", i, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func parseMessage(raw json.RawMessage) (lipapi.Message, error) {
	var probe struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"`
		ToolCallID   string          `json:"tool_call_id"`
		ToolCalls    json.RawMessage `json:"tool_calls"`
		FunctionCall json.RawMessage `json:"function_call"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return lipapi.Message{}, err
	}
	role, err := mapRole(probe.Role)
	if err != nil {
		return lipapi.Message{}, err
	}

	switch role {
	case lipapi.RoleTool:
		if strings.TrimSpace(probe.ToolCallID) == "" {
			return lipapi.Message{}, errors.New("tool message requires tool_call_id")
		}
		content, err := parseToolMessageContent(probe.Content)
		if err != nil {
			return lipapi.Message{}, err
		}
		rawJSON, err := json.Marshal(content)
		if err != nil {
			return lipapi.Message{}, err
		}
		return lipapi.Message{
			Role: lipapi.RoleTool,
			Parts: []lipapi.Part{{
				Kind:       lipapi.PartToolResult,
				ToolCallID: strings.TrimSpace(probe.ToolCallID),
				Content:    rawJSON,
			}},
		}, nil
	case lipapi.RoleAssistant:
		parts, err := parseAssistantParts(probe.Content, probe.ToolCalls, probe.FunctionCall)
		if err != nil {
			return lipapi.Message{}, err
		}
		return lipapi.Message{Role: lipapi.RoleAssistant, Parts: parts}, nil
	}

	parts, err := parseChatContent(probe.Content)
	if err != nil {
		return lipapi.Message{}, err
	}
	return lipapi.Message{Role: role, Parts: parts}, nil
}

func parseAssistantParts(content, toolCalls, functionCall json.RawMessage) ([]lipapi.Part, error) {
	var parts []lipapi.Part
	if jsonutil.IsPresentNonNullJSON(content) {
		cp, err := parseChatContent(content)
		if err != nil {
			return nil, err
		}
		parts = append(parts, cp...)
	}
	if jsonutil.IsPresentNonNullJSON(toolCalls) {
		var rawCalls []json.RawMessage
		if err := json.Unmarshal(toolCalls, &rawCalls); err != nil {
			return nil, fmt.Errorf("openailegacy: tool_calls: %w", err)
		}
		for _, rc := range rawCalls {
			if !json.Valid(rc) {
				return nil, errors.New("openailegacy: invalid tool_calls entry")
			}
			parts = append(parts, lipapi.Part{Kind: lipapi.PartJSON, Content: append(json.RawMessage(nil), rc...)})
		}
	}
	if jsonutil.IsPresentNonNullJSON(functionCall) {
		if !json.Valid(functionCall) {
			return nil, errors.New("openailegacy: invalid function_call")
		}
		parts = append(parts, lipapi.Part{Kind: lipapi.PartJSON, Content: append(json.RawMessage(nil), functionCall...)})
	}
	if len(parts) == 0 {
		return nil, errors.New("openailegacy: assistant message requires content, tool_calls, or function_call")
	}
	return parts, nil
}

func parseToolMessageContent(raw json.RawMessage) (any, error) {
	if jsonutil.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("tool message content is required")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("tool message content is required")
		}
		return s, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func mapRole(r string) (lipapi.Role, error) {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "user":
		return lipapi.RoleUser, nil
	case "assistant":
		return lipapi.RoleAssistant, nil
	case "system":
		return lipapi.RoleSystem, nil
	case "developer":
		return lipapi.RoleSystem, nil
	case "tool":
		return lipapi.RoleTool, nil
	case "":
		return "", errors.New("message role is required")
	default:
		return "", fmt.Errorf("unsupported role %q", r)
	}
}

func parseChatContent(raw json.RawMessage) ([]lipapi.Part, error) {
	if jsonutil.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("message content is required")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("message content string is empty")
		}
		return []lipapi.Part{lipapi.TextPart(s)}, nil
	}
	if raw[0] != '[' {
		return nil, errors.New("message content must be a string or array")
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	var parts []lipapi.Part
	for i, blk := range blocks {
		p, err := parseChatContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func parseChatContentBlock(blk map[string]json.RawMessage) (lipapi.Part, error) {
	tRaw, ok := blk["type"]
	if !ok {
		return lipapi.Part{}, errors.New("content block missing type")
	}
	var typ string
	if err := json.Unmarshal(tRaw, &typ); err != nil {
		return lipapi.Part{}, err
	}
	switch strings.TrimSpace(typ) {
	case "text":
		var s struct {
			Text string `json:"text"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return lipapi.Part{}, err
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.Part{}, err
		}
		if strings.TrimSpace(s.Text) == "" {
			return lipapi.Part{}, errors.New("text part requires text")
		}
		return lipapi.TextPart(s.Text), nil
	case "image_url":
		var s struct {
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return lipapi.Part{}, err
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.Part{}, err
		}
		u := strings.TrimSpace(s.ImageURL.URL)
		if u == "" {
			return lipapi.Part{}, errors.New("image_url requires url")
		}
		return openaiwire.ImagePartFromURL(u)
	case "file":
		var s struct {
			File struct {
				FileData string `json:"file_data"`
				Filename string `json:"filename"`
			} `json:"file"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return lipapi.Part{}, err
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.Part{}, err
		}
		fd := strings.TrimSpace(s.File.FileData)
		if fd == "" {
			return lipapi.Part{}, errors.New("file part requires file_data")
		}
		return openaiwire.FilePartFromBase64(s.File.Filename, fd), nil
	default:
		return lipapi.Part{}, fmt.Errorf("unsupported content block type %q", typ)
	}
}

func parseTools(raw json.RawMessage) ([]lipapi.ToolDef, error) {
	if jsonutil.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openailegacy: tools: %w", err)
	}
	out := make([]lipapi.ToolDef, 0, len(items))
	for i, it := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(it, &probe); err != nil {
			return nil, fmt.Errorf("openailegacy: tools[%d]: %w", i, err)
		}
		if probe.Type != "" && probe.Type != "function" {
			return nil, fmt.Errorf("openailegacy: tools[%d]: unsupported type %q", i, probe.Type)
		}
		var w struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			return nil, fmt.Errorf("openailegacy: tools[%d]: %w", i, err)
		}
		if strings.TrimSpace(w.Function.Name) == "" {
			return nil, fmt.Errorf("openailegacy: tools[%d]: function name is required", i)
		}
		params := w.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out = append(out, lipapi.ToolDef{
			Name:        w.Function.Name,
			Description: w.Function.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

func parseToolChoice(raw json.RawMessage) (lipapi.ToolChoice, error) {
	if jsonutil.IsAbsentOrJSONNull(raw) {
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.ToolChoice{}, err
		}
		switch strings.TrimSpace(strings.ToLower(s)) {
		case "auto", "":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
		case "none":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
		case "required":
			// OpenAI: model must call one or more tools. Canonical ToolChoiceRequired
			// always carries a specific tool name; use ToolChoiceAny for "any declared tool".
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
		default:
			return lipapi.ToolChoice{}, fmt.Errorf("openailegacy: unsupported tool_choice string %q", s)
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return lipapi.ToolChoice{}, fmt.Errorf("openailegacy: tool_choice: %w", err)
	}
	switch strings.TrimSpace(obj.Type) {
	case "function":
		name := strings.TrimSpace(obj.Function.Name)
		if name == "" {
			return lipapi.ToolChoice{}, errors.New("openailegacy: tool_choice function name is required")
		}
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: name}, nil
	default:
		return lipapi.ToolChoice{}, fmt.Errorf("openailegacy: unsupported tool_choice type %q", obj.Type)
	}
}

// ModelFromCall returns the wire model string stored during decode.
func ModelFromCall(c *lipapi.Call) string {
	if c == nil || c.Extensions == nil {
		return ""
	}
	raw, ok := c.Extensions[extModelJSONKey]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}
