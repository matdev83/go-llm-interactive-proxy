package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openaiwire"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/sessionwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Extension keys used by this frontend for round-trip metadata.
const (
	extModelJSONKey = "openairesponses.model"
)

// DecodeOptions configures decoding of a Responses API create request.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:gpt-4o-mini"); usually from X-LIP-Route header.
	RouteSelector string
	// Headers carries optional HTTP carriers (e.g. LIP session resume headers).
	Headers http.Header
}

// DecodedCreate is the result of decoding POST /v1/responses JSON.
type DecodedCreate struct {
	Call   *lipapi.Call
	Stream bool
	Model  string
}

type wireCreate struct {
	Model         string          `json:"model"`
	Stream        bool            `json:"stream"`
	Input         json.RawMessage `json:"input"`
	Instructions  json.RawMessage `json:"instructions"`
	Tools         json.RawMessage `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	ParallelTools *bool           `json:"parallel_tool_calls"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	MaxOut        *int            `json:"max_output_tokens"`
	// Metadata may include LIP session keys ([sessionwire.MetaKeyAuthoritativeSessionID]).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// DecodeCreateRequest maps an OpenAI Responses create JSON body into a canonical call.
func DecodeCreateRequest(body []byte, opts DecodeOptions) (*DecodedCreate, error) {
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("openairesponses: route selector is required")
	}
	var w wireCreate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("openairesponses: invalid json: %w", err)
	}
	model := strings.TrimSpace(w.Model)
	if model == "" {
		return nil, errors.New("openairesponses: model is required")
	}
	if len(w.Input) == 0 {
		return nil, errors.New("openairesponses: input is required")
	}

	instructions, err := parseInstructions(w.Instructions)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: instructions: %w", err)
	}
	msgs, err := parseInput(w.Input)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: input: %w", err)
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: tool_choice: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}

	call := &lipapi.Call{
		Route:        lipapi.RouteIntent{Selector: sel},
		Instructions: instructions,
		Messages:     msgs,
		Tools:        tools,
		ToolChoice:   toolChoice,
		Extensions:   ext,
		Options: lipapi.GenerationOptions{
			Temperature:       w.Temperature,
			TopP:              w.TopP,
			MaxOutputTokens:   w.MaxOut,
			ParallelToolCalls: w.ParallelTools,
		},
	}
	if len(w.Metadata) > 0 {
		sessionwire.ApplyMetadata(&call.Session, w.Metadata)
	}
	if opts.Headers != nil {
		sessionwire.ApplyAuthoritativeHeaders(&call.Session, opts.Headers)
	}
	return &DecodedCreate{Call: call, Stream: w.Stream, Model: model}, nil
}

func parseInstructions(raw json.RawMessage) ([]lipapi.Message, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	// String instructions -> single system message.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: instructions: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, nil
		}
		return []lipapi.Message{{
			Role:  lipapi.RoleSystem,
			Parts: []lipapi.Part{lipapi.TextPart(s)},
		}}, nil
	}
	return nil, errors.New("openairesponses: instructions must be a JSON string in this adapter")
}

func parseInput(raw json.RawMessage) ([]lipapi.Message, error) {
	if len(raw) == 0 {
		return nil, errors.New("openairesponses: input is empty")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: input string: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("openairesponses: input string is empty")
		}
		return []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(s)},
		}}, nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("openairesponses: input array: %w", err)
		}
		out := make([]lipapi.Message, 0, len(items))
		for i, it := range items {
			m, err := parseInputItem(it)
			if err != nil {
				return nil, fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	default:
		return nil, errors.New("openairesponses: input must be a string or array")
	}
}

func parseInputItem(raw json.RawMessage) (lipapi.Message, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: input item json: %w", err)
	}
	switch strings.TrimSpace(probe.Type) {
	case "function_call_output":
		return parseFunctionCallOutputItem(raw)
	case "function_call":
		return parseFunctionCallInputItem(raw)
	case "", "message":
		return parseMessageInputItem(raw)
	default:
		return lipapi.Message{}, fmt.Errorf("openairesponses: unsupported input item type %q", probe.Type)
	}
}

func parseMessageInputItem(raw json.RawMessage) (lipapi.Message, error) {
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: message input json: %w", err)
	}
	role, err := mapRole(m.Role)
	if err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: message input role: %w", err)
	}
	parts, err := parseContent(m.Content)
	if err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: message input content: %w", err)
	}
	return lipapi.Message{Role: role, Parts: parts}, nil
}

func parseFunctionCallInputItem(raw json.RawMessage) (lipapi.Message, error) {
	var v struct {
		ID        string          `json:"id"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: function_call json: %w", err)
	}
	if strings.TrimSpace(v.CallID) == "" {
		return lipapi.Message{}, errors.New("openairesponses: function_call requires call_id")
	}
	if strings.TrimSpace(v.Name) == "" {
		return lipapi.Message{}, errors.New("openairesponses: function_call requires name")
	}
	argStr := "{}"
	if jsonpresence.IsPresentNonNullJSON(v.Arguments) {
		switch v.Arguments[0] {
		case '"':
			var s string
			if err := json.Unmarshal(v.Arguments, &s); err != nil {
				return lipapi.Message{}, fmt.Errorf("openairesponses: function_call arguments: %w", err)
			}
			argStr = s
		default:
			if !json.Valid(v.Arguments) {
				return lipapi.Message{}, errors.New("openairesponses: function_call arguments must be JSON")
			}
			argStr = string(v.Arguments)
		}
	}
	wire := map[string]any{
		"type":      "function_call",
		"call_id":   strings.TrimSpace(v.CallID),
		"name":      strings.TrimSpace(v.Name),
		"arguments": argStr,
	}
	if id := strings.TrimSpace(v.ID); id != "" {
		wire["id"] = id
	}
	content, err := json.Marshal(wire)
	if err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: function_call marshal: %w", err)
	}
	return lipapi.Message{
		Role: lipapi.RoleAssistant,
		Parts: []lipapi.Part{{
			Kind:    lipapi.PartJSON,
			Content: content,
		}},
	}, nil
}

func parseFunctionCallOutputItem(raw json.RawMessage) (lipapi.Message, error) {
	var v struct {
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return lipapi.Message{}, fmt.Errorf("openairesponses: function_call_output json: %w", err)
	}
	if strings.TrimSpace(v.CallID) == "" {
		return lipapi.Message{}, errors.New("openairesponses: function_call_output requires call_id")
	}
	out := v.Output
	if jsonpresence.IsAbsentOrJSONNull(out) {
		return lipapi.Message{}, errors.New("openairesponses: function_call_output requires output")
	}
	if out[0] == '"' {
		var s string
		if err := json.Unmarshal(out, &s); err != nil {
			return lipapi.Message{}, fmt.Errorf("openairesponses: function_call_output string: %w", err)
		}
		var err error
		out, err = json.Marshal(s)
		if err != nil {
			return lipapi.Message{}, fmt.Errorf("openairesponses: function_call_output re-marshal: %w", err)
		}
	}
	if !json.Valid(out) {
		return lipapi.Message{}, errors.New("openairesponses: function_call_output output must be JSON")
	}
	return lipapi.Message{
		Role: lipapi.RoleTool,
		Parts: []lipapi.Part{{
			Kind:       lipapi.PartToolResult,
			ToolCallID: strings.TrimSpace(v.CallID),
			Content:    append(json.RawMessage(nil), out...),
		}},
	}, nil
}

func mapRole(r string) (lipapi.Role, error) {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "user":
		return lipapi.RoleUser, nil
	case "assistant":
		return lipapi.RoleAssistant, nil
	case "system", "developer":
		return lipapi.RoleSystem, nil
	case "tool":
		return lipapi.RoleTool, nil
	case "":
		return lipapi.RoleUser, nil
	default:
		return "", fmt.Errorf("openairesponses: unsupported role %q", r)
	}
}

func parseContent(raw json.RawMessage) ([]lipapi.Part, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("message content is required")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: message content string: %w", err)
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
		return nil, fmt.Errorf("openairesponses: message content array: %w", err)
	}
	var parts []lipapi.Part
	for i, blk := range blocks {
		p, err := parseContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("openairesponses: content[%d]: %w", i, err)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func parseContentBlock(blk map[string]json.RawMessage) (lipapi.Part, error) {
	tRaw, ok := blk["type"]
	if !ok {
		return lipapi.Part{}, errors.New("content block missing type")
	}
	var typ string
	if err := json.Unmarshal(tRaw, &typ); err != nil {
		return lipapi.Part{}, fmt.Errorf("openairesponses: content block type: %w", err)
	}
	switch strings.TrimSpace(typ) {
	case "input_text":
		var s struct {
			Text string `json:"text"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return lipapi.Part{}, fmt.Errorf("openairesponses: input_text wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.Part{}, fmt.Errorf("openairesponses: input_text json: %w", err)
		}
		if strings.TrimSpace(s.Text) == "" {
			return lipapi.Part{}, errors.New("input_text requires text")
		}
		return lipapi.TextPart(s.Text), nil
	case "input_image":
		var s struct {
			ImageURL string `json:"image_url"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return lipapi.Part{}, fmt.Errorf("openairesponses: input_image wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.Part{}, fmt.Errorf("openairesponses: input_image json: %w", err)
		}
		if strings.TrimSpace(s.ImageURL) == "" {
			return lipapi.Part{}, errors.New("input_image requires image_url")
		}
		return openaiwire.ImagePartFromURL(s.ImageURL)
	case "input_file":
		var s struct {
			FileData string `json:"file_data"`
			Filename string `json:"filename"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return lipapi.Part{}, fmt.Errorf("openairesponses: input_file wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.Part{}, fmt.Errorf("openairesponses: input_file json: %w", err)
		}
		if strings.TrimSpace(s.FileData) == "" {
			return lipapi.Part{}, errors.New("input_file requires file_data")
		}
		return openaiwire.FilePartFromBase64(s.Filename, s.FileData), nil
	default:
		return lipapi.Part{}, fmt.Errorf("openairesponses: unsupported content block type %q", typ)
	}
}

func parseTools(raw json.RawMessage) ([]lipapi.ToolDef, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openairesponses: tools: %w", err)
	}
	out := make([]lipapi.ToolDef, 0, len(items))
	for i, it := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(it, &probe); err != nil {
			return nil, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
		}
		if probe.Type != "" && probe.Type != "function" {
			return nil, fmt.Errorf("openairesponses: tools[%d]: unsupported type %q", i, probe.Type)
		}
		var w struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
			// Flat shape used by some Responses clients / SDK unions (type + name at top level).
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			return nil, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
		}
		name := strings.TrimSpace(w.Function.Name)
		desc := w.Function.Description
		params := w.Function.Parameters
		if name == "" {
			name = strings.TrimSpace(w.Name)
			if desc == "" {
				desc = w.Description
			}
			if len(params) == 0 {
				params = w.Parameters
			}
		}
		if name == "" {
			return nil, fmt.Errorf("openairesponses: tools[%d]: function name is required", i)
		}
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out = append(out, lipapi.ToolDef{
			Name:        name,
			Description: desc,
			Parameters:  params,
		})
	}
	return out, nil
}

func parseToolChoice(raw json.RawMessage) (lipapi.ToolChoice, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return lipapi.ToolChoice{}, fmt.Errorf("openairesponses: tool_choice string json: %w", err)
		}
		switch strings.TrimSpace(strings.ToLower(s)) {
		case "auto", "":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
		case "none":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
		case "required":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
		default:
			return lipapi.ToolChoice{}, fmt.Errorf("openairesponses: unsupported tool_choice string %q", s)
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return lipapi.ToolChoice{}, fmt.Errorf("openairesponses: tool_choice: %w", err)
	}
	switch strings.TrimSpace(obj.Type) {
	case "function":
		name := strings.TrimSpace(obj.Function.Name)
		if name == "" {
			return lipapi.ToolChoice{}, errors.New("openairesponses: tool_choice function name is required")
		}
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: name}, nil
	default:
		return lipapi.ToolChoice{}, fmt.Errorf("openairesponses: unsupported tool_choice type %q", obj.Type)
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
