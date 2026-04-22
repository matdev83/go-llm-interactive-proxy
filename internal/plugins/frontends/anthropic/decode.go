package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Extension keys for round-trip metadata on the canonical call.
const (
	extModelJSONKey = "anthropic.model"
)

// DecodeOptions configures decoding of a Messages API request.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:claude-3-5-haiku"); usually from X-LIP-Route header.
	RouteSelector string
	// AnthropicVersion is the optional anthropic-version header; empty means default for decode.
	AnthropicVersion string
}

// DecodedMessage is the result of decoding POST /v1/messages JSON.
type DecodedMessage struct {
	Call   *lipapi.Call
	Stream bool
	Model  string
}

type wireCreate struct {
	Model       string            `json:"model"`
	MaxTokens   int               `json:"max_tokens"`
	Messages    []json.RawMessage `json:"messages"`
	Stream      bool              `json:"stream"`
	System      json.RawMessage   `json:"system"`
	Metadata    json.RawMessage   `json:"metadata"`
	Temperature *float64          `json:"temperature"`
	TopP        *float64          `json:"top_p"`
	TopK        *int              `json:"top_k"`
	Tools       json.RawMessage   `json:"tools"`
	ToolChoice  json.RawMessage   `json:"tool_choice"`
}

// DecodeMessageRequest maps Anthropic Messages JSON into a canonical call.
func DecodeMessageRequest(body []byte, opts DecodeOptions) (*DecodedMessage, error) {
	_ = opts.AnthropicVersion // optional header; wire compatibility (see package doc).
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("anthropic: route selector is required")
	}
	var w wireCreate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("anthropic: invalid json: %w", err)
	}
	model := strings.TrimSpace(w.Model)
	if model == "" {
		return nil, errors.New("anthropic: model is required")
	}
	if w.MaxTokens <= 0 {
		return nil, errors.New("anthropic: max_tokens is required and must be positive")
	}
	if len(w.Messages) == 0 {
		return nil, errors.New("anthropic: messages is required")
	}

	instructions, err := parseSystem(w.System)
	if err != nil {
		return nil, fmt.Errorf("anthropic: system: %w", err)
	}
	msgs, err := parseAnthropicMessages(w.Messages)
	if err != nil {
		return nil, fmt.Errorf("anthropic: messages: %w", err)
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, fmt.Errorf("anthropic: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("anthropic: tool_choice: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}

	maxOut := w.MaxTokens
	call := &lipapi.Call{
		Route:        lipapi.RouteIntent{Selector: sel},
		Instructions: instructions,
		Messages:     msgs,
		Tools:        tools,
		ToolChoice:   toolChoice,
		Extensions:   ext,
		Options: lipapi.GenerationOptions{
			Temperature:     w.Temperature,
			TopP:            w.TopP,
			MaxOutputTokens: &maxOut,
		},
	}
	_ = w.Metadata
	_ = w.TopK
	return &DecodedMessage{Call: call, Stream: w.Stream, Model: model}, nil
}

func parseSystem(raw json.RawMessage) ([]lipapi.Message, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	// Plain string system prompt.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
		return []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(s)},
		}}, nil
	}
	// Array of content blocks (text only for v1 subset).
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("anthropic: system: %w", err)
	}
	var parts []lipapi.Part
	for i, blk := range blocks {
		p, err := parseContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("anthropic: system block[%d]: %w", i, err)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return []lipapi.Message{{Role: lipapi.RoleSystem, Parts: parts}}, nil
}

func parseAnthropicMessages(raw []json.RawMessage) ([]lipapi.Message, error) {
	out := make([]lipapi.Message, 0, len(raw))
	for i, it := range raw {
		m, err := parseAnthropicMessage(it)
		if err != nil {
			return nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func parseAnthropicMessage(raw json.RawMessage) (lipapi.Message, error) {
	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return lipapi.Message{}, err
	}
	role, err := mapAnthropicRole(probe.Role)
	if err != nil {
		return lipapi.Message{}, err
	}
	parts, err := parseMessageContent(probe.Content)
	if err != nil {
		return lipapi.Message{}, err
	}
	return lipapi.Message{Role: role, Parts: parts}, nil
}

func mapAnthropicRole(r string) (lipapi.Role, error) {
	switch strings.TrimSpace(r) {
	case "user":
		return lipapi.RoleUser, nil
	case "assistant":
		return lipapi.RoleAssistant, nil
	default:
		return "", fmt.Errorf("anthropic: unsupported message role %q", r)
	}
}

func parseMessageContent(raw json.RawMessage) ([]lipapi.Part, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("anthropic: message content is required")
	}
	// Shorthand string content.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("anthropic: empty text content")
		}
		return []lipapi.Part{lipapi.TextPart(s)}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("anthropic: content: %w", err)
	}
	out := make([]lipapi.Part, 0, len(blocks))
	for i, blk := range blocks {
		p, err := parseContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("anthropic: content[%d]: %w", i, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("anthropic: content blocks are empty")
	}
	return out, nil
}

func parseContentBlock(blk json.RawMessage) (lipapi.Part, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(blk, &probe); err != nil {
		return lipapi.Part{}, err
	}
	switch probe.Type {
	case "text":
		var s struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(blk, &s); err != nil {
			return lipapi.Part{}, err
		}
		t := strings.TrimSpace(s.Text)
		if t == "" {
			return lipapi.Part{}, errors.New("anthropic: text block requires text")
		}
		return lipapi.TextPart(t), nil
	case "image":
		var w struct {
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}
		if err := json.Unmarshal(blk, &w); err != nil {
			return lipapi.Part{}, err
		}
		if w.Source.Type != "base64" {
			return lipapi.Part{}, fmt.Errorf("anthropic: image source type %q not supported", w.Source.Type)
		}
		data := strings.TrimSpace(w.Source.Data)
		if data == "" {
			return lipapi.Part{}, errors.New("anthropic: image requires base64 data")
		}
		mime := strings.TrimSpace(w.Source.MediaType)
		if mime == "" {
			mime = "image/png"
		}
		ref := "data:" + mime + ";base64," + data
		return lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: ref, ImageMIME: mime}, nil
	case "document":
		var w struct {
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}
		if err := json.Unmarshal(blk, &w); err != nil {
			return lipapi.Part{}, err
		}
		if w.Source.Type != "base64" {
			return lipapi.Part{}, fmt.Errorf("anthropic: document source type %q not supported", w.Source.Type)
		}
		data := strings.TrimSpace(w.Source.Data)
		if data == "" {
			return lipapi.Part{}, errors.New("anthropic: document requires base64 data")
		}
		mime := strings.TrimSpace(w.Source.MediaType)
		if mime == "" {
			mime = "application/pdf"
		}
		name := "document"
		if mime == "application/pdf" {
			name = "document.pdf"
		}
		ref := "data:" + mime + ";base64," + data
		return lipapi.FilePart(ref, mime, name), nil
	case "tool_use":
		var w struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(blk, &w); err != nil {
			return lipapi.Part{}, err
		}
		if strings.TrimSpace(w.ID) == "" {
			return lipapi.Part{}, errors.New("anthropic: tool_use requires id")
		}
		if strings.TrimSpace(w.Name) == "" {
			return lipapi.Part{}, errors.New("anthropic: tool_use requires name")
		}
		input := w.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return lipapi.Part{
			Kind:       lipapi.PartJSON,
			ToolCallID: w.ID,
			ToolName:   w.Name,
			Content:    input,
		}, nil
	case "tool_result":
		var w struct {
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(blk, &w); err != nil {
			return lipapi.Part{}, err
		}
		if strings.TrimSpace(w.ToolUseID) == "" {
			return lipapi.Part{}, errors.New("anthropic: tool_result requires tool_use_id")
		}
		// Content may be a string or array of text blocks; flatten to plain text.
		var resultText string
		if jsonpresence.IsPresentNonNullJSON(w.Content) {
			var s string
			if err := json.Unmarshal(w.Content, &s); err == nil {
				resultText = s
			} else {
				var blocks []json.RawMessage
				if err := json.Unmarshal(w.Content, &blocks); err == nil {
					var sb strings.Builder
					for _, b := range blocks {
						var tb struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}
						if err := json.Unmarshal(b, &tb); err == nil && tb.Type == "text" {
							sb.WriteString(tb.Text)
						}
					}
					resultText = sb.String()
				}
			}
		}
		return lipapi.Part{
			Kind:       lipapi.PartToolResult,
			ToolCallID: w.ToolUseID,
			Text:       resultText,
		}, nil
	default:
		return lipapi.Part{}, fmt.Errorf("anthropic: unsupported content block type %q", probe.Type)
	}
}

func parseTools(raw json.RawMessage) ([]lipapi.ToolDef, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("anthropic: tools: %w", err)
	}
	out := make([]lipapi.ToolDef, 0, len(items))
	for i, it := range items {
		var w struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d]: %w", i, err)
		}
		if strings.TrimSpace(w.Name) == "" {
			return nil, fmt.Errorf("anthropic: tools[%d]: name is required", i)
		}
		params := w.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out = append(out, lipapi.ToolDef{
			Name:        w.Name,
			Description: w.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

func parseToolChoice(raw json.RawMessage) (lipapi.ToolChoice, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.TrimSpace(s) {
		case "", "auto":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
		case "none":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
		case "any":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
		default:
			return lipapi.ToolChoice{}, fmt.Errorf("anthropic: unsupported tool_choice string %q", s)
		}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return lipapi.ToolChoice{}, fmt.Errorf("anthropic: tool_choice: %w", err)
	}
	switch obj.Type {
	case "auto", "":
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	case "none":
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
	case "any":
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
	case "tool":
		if strings.TrimSpace(obj.Name) == "" {
			return lipapi.ToolChoice{}, errors.New("anthropic: tool_choice tool requires name")
		}
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: obj.Name}, nil
	default:
		return lipapi.ToolChoice{}, fmt.Errorf("anthropic: unsupported tool_choice type %q", obj.Type)
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
