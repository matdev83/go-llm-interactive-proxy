package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Extension key for wire model stored by the OpenAI Responses frontend decoder.
const extModelJSONKey = "openairesponses.model"

func resolveModel(cand routing.AttemptCandidate, call lipapi.Call) string {
	m := strings.TrimSpace(cand.Primary.Model)
	if m != "" {
		return m
	}
	if call.Extensions != nil {
		raw, ok := call.Extensions[extModelJSONKey]
		if ok && len(raw) > 0 {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// ParamsForCall builds an OpenAI Responses create payload from a canonical call.
func ParamsForCall(call *lipapi.Call, cand routing.AttemptCandidate) (responses.ResponseNewParams, error) {
	if call == nil {
		return responses.ResponseNewParams{}, fmt.Errorf("openairesponses: nil call")
	}
	model := resolveModel(cand, *call)
	if model == "" {
		return responses.ResponseNewParams{}, fmt.Errorf("openairesponses: model is required (route candidate or %s extension)", extModelJSONKey)
	}

	p := responses.ResponseNewParams{
		Model: shared.ResponsesModel(model),
	}

	if inst := joinInstructionText(call.Instructions); inst != "" {
		p.Instructions = openai.String(inst)
	}

	items, err := buildInputItems(call)
	if err != nil {
		return responses.ResponseNewParams{}, fmt.Errorf("openairesponses: build input items: %w", err)
	}
	p.Input = responses.ResponseNewParamsInputUnion{
		OfInputItemList: items,
	}

	if len(call.Tools) > 0 {
		tools, err := buildTools(call.Tools)
		if err != nil {
			return responses.ResponseNewParams{}, fmt.Errorf("openairesponses: build tools: %w", err)
		}
		p.Tools = tools
		p.ToolChoice = toolChoiceUnion(call.ToolChoice, len(call.Tools))
	}

	o := call.Options
	if o.Temperature != nil {
		p.Temperature = openai.Float(*o.Temperature)
	}
	if o.TopP != nil {
		p.TopP = openai.Float(*o.TopP)
	}
	if o.MaxOutputTokens != nil {
		p.MaxOutputTokens = openai.Int(int64(*o.MaxOutputTokens))
	}
	if o.ParallelToolCalls != nil {
		p.ParallelToolCalls = openai.Bool(*o.ParallelToolCalls)
	}
	if e := strings.TrimSpace(o.ReasoningEffort); e != "" {
		p.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(e)}
	}

	return p, nil
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

func buildInputItems(call *lipapi.Call) ([]responses.ResponseInputItemUnionParam, error) {
	var items []responses.ResponseInputItemUnionParam
	for _, m := range call.Messages {
		if m.Role == lipapi.RoleTool {
			for _, p := range m.Parts {
				if p.Kind != lipapi.PartToolResult {
					return nil, fmt.Errorf("openairesponses: tool message part kind %q not supported", p.Kind)
				}
				out := toolResultString(p)
				items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(p.ToolCallID, out))
			}
			continue
		}
		if m.Role == lipapi.RoleAssistant && len(m.Parts) > 0 {
			fcs, ok := assistantWireFunctionCalls(m.Parts)
			if ok {
				items = append(items, fcs...)
				continue
			}
		}
		it, err := messageToInputItem(m)
		if err != nil {
			return nil, fmt.Errorf("openairesponses: input item: %w", err)
		}
		items = append(items, it)
	}
	return items, nil
}

// assistantWireFunctionCalls maps assistant-only PartJSON items produced by the
// Responses frontend decoder for wire type "function_call" into SDK input items.
func assistantWireFunctionCalls(parts []lipapi.Part) ([]responses.ResponseInputItemUnionParam, bool) {
	out := make([]responses.ResponseInputItemUnionParam, 0, len(parts))
	for _, p := range parts {
		it, ok := partToFunctionCallInputItem(p)
		if !ok {
			return nil, false
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func partToFunctionCallInputItem(p lipapi.Part) (responses.ResponseInputItemUnionParam, bool) {
	if p.Kind != lipapi.PartJSON || len(p.Content) == 0 {
		return responses.ResponseInputItemUnionParam{}, false
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(p.Content, &probe); err != nil {
		return responses.ResponseInputItemUnionParam{}, false
	}
	if t := strings.TrimSpace(probe.Type); t != "" && t != "function_call" {
		return responses.ResponseInputItemUnionParam{}, false
	}
	var v struct {
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(p.Content, &v); err != nil {
		return responses.ResponseInputItemUnionParam{}, false
	}
	callID := strings.TrimSpace(v.CallID)
	name := strings.TrimSpace(v.Name)
	if callID == "" || name == "" {
		return responses.ResponseInputItemUnionParam{}, false
	}
	argStr := "{}"
	if jsonpresence.IsPresentNonNullJSON(v.Arguments) {
		switch v.Arguments[0] {
		case '"':
			var s string
			if err := json.Unmarshal(v.Arguments, &s); err != nil {
				return responses.ResponseInputItemUnionParam{}, false
			}
			argStr = s
		default:
			if !json.Valid(v.Arguments) {
				return responses.ResponseInputItemUnionParam{}, false
			}
			argStr = string(v.Arguments)
		}
	}
	return responses.ResponseInputItemParamOfFunctionCall(argStr, callID, name), true
}

func toolResultString(p lipapi.Part) string {
	if len(p.Content) == 0 {
		return ""
	}
	return string(p.Content)
}

func messageToInputItem(m lipapi.Message) (responses.ResponseInputItemUnionParam, error) {
	role, err := mapEasyRole(m.Role)
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, err
	}
	if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText {
		return responses.ResponseInputItemParamOfMessage(m.Parts[0].Text, role), nil
	}
	list, err := partsToContentList(m.Parts)
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, err
	}
	// Multimodal / structured content uses the explicit input_message shape.
	rs := roleString(m.Role)
	return responses.ResponseInputItemParamOfInputMessage(list, rs), nil
}

func mapEasyRole(r lipapi.Role) (responses.EasyInputMessageRole, error) {
	switch r {
	case lipapi.RoleUser:
		return responses.EasyInputMessageRoleUser, nil
	case lipapi.RoleAssistant:
		return responses.EasyInputMessageRoleAssistant, nil
	case lipapi.RoleSystem:
		return responses.EasyInputMessageRoleSystem, nil
	default:
		return "", fmt.Errorf("openairesponses: unsupported message role %q for simple message mapping", r)
	}
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

func partsToContentList(parts []lipapi.Part) (responses.ResponseInputMessageContentListParam, error) {
	out := make(responses.ResponseInputMessageContentListParam, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, responses.ResponseInputContentParamOfInputText(p.Text))
		case lipapi.PartImageRef:
			img := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
			img.OfInputImage.ImageURL = openai.String(p.ImageRef)
			out = append(out, img)
		case lipapi.PartFileRef:
			b64, fname, err := fileDataFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, responses.ResponseInputContentUnionParam{
				OfInputFile: &responses.ResponseInputFileParam{
					FileData: openai.String(b64),
					Filename: openai.String(fname),
				},
			})
		default:
			return nil, fmt.Errorf("openairesponses: unsupported part kind %q in message content", p.Kind)
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
			return "", "", fmt.Errorf("openairesponses: invalid data URL in file part")
		}
		return b64, filename, nil
	}
	return "", "", fmt.Errorf("openairesponses: file part requires a data URL, got %q", ref)
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

func buildTools(tools []lipapi.ToolDef) ([]responses.ToolUnionParam, error) {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("openairesponses: tool %q parameters: %w", t.Name, err)
			}
		}
		if schema == nil {
			schema = map[string]any{}
		}
		out = append(out, responses.ToolParamOfFunction(t.Name, schema, true))
	}
	return out, nil
}

func toolChoiceUnion(tc lipapi.ToolChoice, nTools int) responses.ResponseNewParamsToolChoiceUnion {
	mode := tc.Mode
	if mode == "" {
		mode = lipapi.ToolChoiceAuto
	}
	switch mode {
	case lipapi.ToolChoiceAuto:
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
	case lipapi.ToolChoiceNone:
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsNone)}
	case lipapi.ToolChoiceAny:
		// Maps to OpenAI "required" — model must invoke a tool.
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsRequired)}
	case lipapi.ToolChoiceRequired:
		if tc.Name != "" && nTools > 0 {
			return responses.ResponseNewParamsToolChoiceUnion{
				OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: tc.Name},
			}
		}
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsRequired)}
	default:
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
	}
}
