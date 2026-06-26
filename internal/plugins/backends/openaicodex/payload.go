package openaicodex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const defaultCodexInstruction = "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer."

type Payload struct {
	Model             string         `json:"model"`
	Stream            bool           `json:"stream"`
	Store             bool           `json:"store"`
	Instructions      string         `json:"instructions"`
	Input             []inputItem    `json:"input"`
	Tools             []toolPayload  `json:"tools,omitempty"`
	Reasoning         *reasoningSpec `json:"reasoning,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey    string         `json:"prompt_cache_key,omitempty"`
}

type inputItem interface {
	inputItem()
}

type textMessageItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (textMessageItem) inputItem() {}

type richMessageItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

func (richMessageItem) inputItem() {}

type functionCallOutputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func (functionCallOutputItem) inputItem() {}

type functionCallItem struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (functionCallItem) inputItem() {}

type contentBlock interface {
	contentBlock()
}

type inputTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (inputTextPart) contentBlock() {}

type inputImagePart struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

func (inputImagePart) contentBlock() {}

type inputFilePart struct {
	Type     string `json:"type"`
	FileData string `json:"file_data"`
	Filename string `json:"filename"`
}

func (inputFilePart) contentBlock() {}

type reasoningSpec struct {
	Effort string `json:"effort"`
}

type toolPayload struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

func PayloadForCall(call *lipapi.Call, cand routing.AttemptCandidate, cfg Config) (Payload, error) {
	if call == nil {
		return Payload{}, fmt.Errorf("%s: nil call", ID)
	}
	model := strings.TrimSpace(cand.Primary.Model)
	if model == "" {
		return Payload{}, fmt.Errorf("%s: model is required", ID)
	}
	items, err := buildInputItems(call)
	if err != nil {
		return Payload{}, err
	}
	p := Payload{
		Model:        model,
		Stream:       true,
		Instructions: resolveInstructions(call.Instructions),
		Input:        items,
	}
	if len(call.Tools) > 0 {
		tools, err := buildTools(call.Tools)
		if err != nil {
			return Payload{}, err
		}
		p.Tools = tools
	}
	if effort := strings.TrimSpace(call.Options.ReasoningEffort); effort != "" {
		p.Reasoning = &reasoningSpec{Effort: effort}
	} else if effort = strings.TrimSpace(cfg.DefaultReasoningEffort); effort != "" {
		p.Reasoning = &reasoningSpec{Effort: effort}
	}
	if call.Options.Temperature != nil {
		return Payload{}, fmt.Errorf("%s: temperature is not supported by Codex", ID)
	}
	if call.Options.MaxOutputTokens != nil && !hasAnthropicModelExtension(call) {
		return Payload{}, fmt.Errorf("%s: max output tokens are not supported by Codex", ID)
	}
	if call.Options.TopP != nil {
		return Payload{}, fmt.Errorf("%s: top_p is not supported by Codex", ID)
	}
	if call.Options.ParallelToolCalls != nil {
		p.ParallelToolCalls = call.Options.ParallelToolCalls
	}
	return p, nil
}

func resolveInstructions(insts []lipapi.Message) string {
	if text := joinInstructionText(insts); text != "" {
		return text
	}
	return defaultCodexInstruction
}

func joinInstructionText(insts []lipapi.Message) string {
	var b strings.Builder
	for _, m := range insts {
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText {
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func hasAnthropicModelExtension(call *lipapi.Call) bool {
	if call == nil || call.Extensions == nil {
		return false
	}
	_, ok := call.Extensions["anthropic.model"]
	return ok
}

func buildInputItems(call *lipapi.Call) ([]inputItem, error) {
	out := make([]inputItem, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role == lipapi.RoleTool {
			for _, p := range m.Parts {
				if p.Kind != lipapi.PartToolResult {
					return nil, fmt.Errorf("%s: unsupported tool part kind %q", ID, p.Kind)
				}
				out = append(out, functionCallOutputItem{
					Type:   "function_call_output",
					CallID: p.ToolCallID,
					Output: toolResultString(p),
				})
			}
			continue
		}
		if m.Role == lipapi.RoleAssistant && len(m.Parts) > 0 {
			fcs, ok, err := assistantFunctionCallItems(m.Parts)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, fcs...)
				continue
			}
		}
		item, err := messageToInputItem(m)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func assistantFunctionCallItems(parts []lipapi.Part) ([]inputItem, bool, error) {
	out := make([]inputItem, 0, len(parts))
	for _, p := range parts {
		item, ok, err := partToFunctionCallItem(p)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, false, nil
	}
	return out, true, nil
}

func partToFunctionCallItem(p lipapi.Part) (inputItem, bool, error) {
	if p.Kind != lipapi.PartJSON || len(p.Content) == 0 {
		return nil, false, nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(p.Content, &probe); err != nil {
		return nil, false, nil
	}
	if t := strings.TrimSpace(probe.Type); t != "" && t != "function_call" {
		return nil, false, nil
	}
	var v struct {
		ID        string          `json:"id"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(p.Content, &v); err != nil {
		return nil, false, fmt.Errorf("%s: function_call json: %w", ID, err)
	}
	callID := strings.TrimSpace(v.CallID)
	name := strings.TrimSpace(v.Name)
	if callID == "" || name == "" {
		return nil, false, fmt.Errorf("%s: function_call requires call_id and name", ID)
	}
	argStr := "{}"
	if jsonpresence.IsPresentNonNullJSON(v.Arguments) {
		switch v.Arguments[0] {
		case '"':
			var s string
			if err := json.Unmarshal(v.Arguments, &s); err != nil {
				return nil, false, fmt.Errorf("%s: function_call arguments: %w", ID, err)
			}
			argStr = s
		default:
			if !json.Valid(v.Arguments) {
				return nil, false, fmt.Errorf("%s: function_call arguments must be JSON", ID)
			}
			argStr = string(v.Arguments)
		}
	}
	item := functionCallItem{
		Type:      "function_call",
		CallID:    callID,
		Name:      name,
		Arguments: argStr,
	}
	if id := strings.TrimSpace(v.ID); id != "" {
		item.ID = id
	}
	return item, true, nil
}

func toolResultString(p lipapi.Part) string {
	if len(p.Content) == 0 {
		return ""
	}
	return string(p.Content)
}

func messageToInputItem(m lipapi.Message) (inputItem, error) {
	role := roleString(m.Role)
	if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText {
		return textMessageItem{
			Type:    "message",
			Role:    role,
			Content: m.Parts[0].Text,
		}, nil
	}
	content, err := partsToContentList(m.Parts)
	if err != nil {
		return nil, err
	}
	return richMessageItem{
		Type:    "message",
		Role:    role,
		Content: content,
	}, nil
}

func roleString(r lipapi.Role) string {
	switch r {
	case lipapi.RoleUser:
		return "user"
	case lipapi.RoleAssistant:
		return "assistant"
	case lipapi.RoleSystem:
		return "system"
	default:
		return "user"
	}
}

func partsToContentList(parts []lipapi.Part) ([]contentBlock, error) {
	out := make([]contentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, inputTextPart{Type: "input_text", Text: p.Text})
		case lipapi.PartImageRef:
			out = append(out, inputImagePart{
				Type:     "input_image",
				ImageURL: p.ImageRef,
			})
		case lipapi.PartFileRef:
			b64, fname, err := fileDataFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, inputFilePart{
				Type:     "input_file",
				FileData: b64,
				Filename: fname,
			})
		default:
			return nil, fmt.Errorf("%s: unsupported part kind %q", ID, p.Kind)
		}
	}
	return out, nil
}

func fileDataFromPart(p lipapi.Part) (dataB64, filename string, err error) {
	filename = strings.TrimSpace(p.FileName)
	ref := p.FileRef
	if strings.HasPrefix(ref, "data:") {
		_, b64, ok := stripDataURLBase64(ref)
		if !ok {
			return "", "", fmt.Errorf("%s: invalid data URL in file part", ID)
		}
		return b64, filename, nil
	}
	return "", "", fmt.Errorf("%s: file part requires a data URL", ID)
}

func stripDataURLBase64(dataURL string) (mime, b64 string, ok bool) {
	rest, ok := strings.CutPrefix(dataURL, "data:")
	if !ok {
		return "", "", false
	}
	mime, enc, found := strings.Cut(rest, ";")
	if !found {
		return "", "", false
	}
	const prefix = "base64,"
	encBody, ok := strings.CutPrefix(enc, prefix)
	if !ok {
		return "", "", false
	}
	return mime, encBody, true
}

func buildTools(tools []lipapi.ToolDef) ([]toolPayload, error) {
	out := make([]toolPayload, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("%s: tool %q parameters: %w", ID, t.Name, err)
			}
		}
		if schema == nil {
			schema = map[string]any{}
		}
		out = append(out, toolPayload{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schema,
			Strict:      true,
		})
	}
	return out, nil
}
