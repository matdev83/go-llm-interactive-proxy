package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
		return nil, err
	}
	msgs, err := parseInput(w.Input)
	if err != nil {
		return nil, err
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, err
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return nil, err
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, err
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
	return &DecodedCreate{Call: call, Stream: w.Stream, Model: model}, nil
}

func parseInstructions(raw json.RawMessage) ([]lipapi.Message, error) {
	if len(raw) == 0 || string(raw) == "null" {
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
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &probe)
	t := strings.TrimSpace(probe.Type)
	if t != "" && t != "message" {
		return lipapi.Message{}, fmt.Errorf("unsupported input item type %q", t)
	}
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return lipapi.Message{}, err
	}
	role, err := mapRole(m.Role)
	if err != nil {
		return lipapi.Message{}, err
	}
	parts, err := parseContent(m.Content)
	if err != nil {
		return lipapi.Message{}, err
	}
	return lipapi.Message{Role: role, Parts: parts}, nil
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
		return "", fmt.Errorf("unsupported role %q", r)
	}
}

func parseContent(raw json.RawMessage) ([]lipapi.Part, error) {
	if len(raw) == 0 || string(raw) == "null" {
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
		p, err := parseContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
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
		return lipapi.Part{}, err
	}
	switch strings.TrimSpace(typ) {
	case "input_text":
		var s struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(mustJSON(blk), &s); err != nil {
			return lipapi.Part{}, err
		}
		if strings.TrimSpace(s.Text) == "" {
			return lipapi.Part{}, errors.New("input_text requires text")
		}
		return lipapi.TextPart(s.Text), nil
	case "input_image":
		var s struct {
			ImageURL string `json:"image_url"`
		}
		if err := json.Unmarshal(mustJSON(blk), &s); err != nil {
			return lipapi.Part{}, err
		}
		if strings.TrimSpace(s.ImageURL) == "" {
			return lipapi.Part{}, errors.New("input_image requires image_url")
		}
		return imagePartFromURL(s.ImageURL)
	case "input_file":
		var s struct {
			FileData string `json:"file_data"`
			Filename string `json:"filename"`
		}
		if err := json.Unmarshal(mustJSON(blk), &s); err != nil {
			return lipapi.Part{}, err
		}
		if strings.TrimSpace(s.FileData) == "" {
			return lipapi.Part{}, errors.New("input_file requires file_data")
		}
		return filePartFromInputFile(s.Filename, s.FileData), nil
	default:
		return lipapi.Part{}, fmt.Errorf("unsupported content block type %q", typ)
	}
}

func mustJSON(blk map[string]json.RawMessage) []byte {
	b, err := json.Marshal(blk)
	if err != nil {
		panic("mustJSON: " + err.Error())
	}
	return b
}

func imagePartFromURL(imageURL string) (lipapi.Part, error) {
	p := lipapi.Part{Kind: lipapi.PartImageRef, ImageRef: imageURL}
	if strings.HasPrefix(imageURL, "data:") {
		rest := strings.TrimPrefix(imageURL, "data:")
		semi := strings.Index(rest, ";")
		if semi > 0 {
			p.ImageMIME = rest[:semi]
		}
	}
	return p, nil
}

func filePartFromInputFile(filename, fileData string) lipapi.Part {
	mime := "application/octet-stream"
	low := strings.ToLower(strings.TrimSpace(filename))
	if strings.HasSuffix(low, ".pdf") {
		mime = "application/pdf"
	}
	ref := "data:" + mime + ";base64," + fileData
	return lipapi.FilePart(ref, mime, filename)
}

func parseTools(raw json.RawMessage) ([]lipapi.ToolDef, error) {
	if len(raw) == 0 || string(raw) == "null" {
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
	if len(raw) == 0 || string(raw) == "null" {
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
