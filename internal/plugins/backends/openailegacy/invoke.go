package openailegacy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// Extension key for wire model stored by the legacy OpenAI Chat frontend decoder.
const (
	extModelJSONKey      = "openailegacy.model"
	extStreamOptsJSONKey = "openailegacy.stream_options"
)

func newSDKClient(cfg Config) openai.Client {
	opts := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return openai.NewClient(opts...)
}

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

func resolveIncludeUsage(call lipapi.Call) bool {
	if call.Extensions == nil {
		return false
	}
	raw, ok := call.Extensions[extStreamOptsJSONKey]
	if !ok || len(raw) == 0 {
		return false
	}
	var opts struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if json.Unmarshal(raw, &opts) != nil {
		return false
	}
	return opts.IncludeUsage
}

// ParamsForCall builds an OpenAI Chat Completions create payload from a canonical call.
func ParamsForCall(call *lipapi.Call, cand routing.AttemptCandidate) (openai.ChatCompletionNewParams, error) {
	if call == nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: nil call")
	}
	model := resolveModel(cand, *call)
	if model == "" {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: model is required (route candidate or %s extension)", extModelJSONKey)
	}

	msgs, err := buildChatMessages(call)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	p := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model),
		Messages: msgs,
	}

	if includeUsage := resolveIncludeUsage(*call); includeUsage {
		p.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
	}

	if len(call.Tools) > 0 {
		tools, err := buildChatTools(call.Tools)
		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}
		p.Tools = tools
		p.ToolChoice = chatToolChoiceUnion(call.ToolChoice, len(call.Tools))
	}

	o := call.Options
	if o.Temperature != nil {
		p.Temperature = openai.Float(*o.Temperature)
	}
	if o.TopP != nil {
		p.TopP = openai.Float(*o.TopP)
	}
	if o.MaxOutputTokens != nil {
		p.MaxTokens = openai.Int(int64(*o.MaxOutputTokens))
	}
	if o.ParallelToolCalls != nil {
		p.ParallelToolCalls = openai.Bool(*o.ParallelToolCalls)
	}
	if e := strings.TrimSpace(o.ReasoningEffort); e != "" {
		p.ReasoningEffort = shared.ReasoningEffort(e)
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

func buildChatMessages(call *lipapi.Call) ([]openai.ChatCompletionMessageParamUnion, error) {
	var out []openai.ChatCompletionMessageParamUnion

	if inst := joinInstructionText(call.Instructions); inst != "" {
		out = append(out, openai.SystemMessage(inst))
	}

	for _, m := range call.Messages {
		u, err := messageToChatParam(m)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

func messageToChatParam(m lipapi.Message) (openai.ChatCompletionMessageParamUnion, error) {
	switch m.Role {
	case lipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != lipapi.PartToolResult {
			return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: tool message must contain one tool_result part")
		}
		p := m.Parts[0]
		return openai.ToolMessage(toolResultString(p), p.ToolCallID), nil

	case lipapi.RoleAssistant:
		if err := assertAssistantPartsSupported(m.Parts); err != nil {
			return openai.ChatCompletionMessageParamUnion{}, err
		}
		if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText {
			return openai.AssistantMessage(m.Parts[0].Text), nil
		}
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: assistant message must be plain text for this adapter")

	case lipapi.RoleUser, lipapi.RoleSystem:
		return userOrSystemChatMessage(m)

	default:
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: unsupported message role %q", m.Role)
	}
}

func assertAssistantPartsSupported(parts []lipapi.Part) error {
	for _, p := range parts {
		if p.Kind != lipapi.PartText {
			return fmt.Errorf("openailegacy: assistant message part kind %q not supported", p.Kind)
		}
	}
	return nil
}

func userOrSystemChatMessage(m lipapi.Message) (openai.ChatCompletionMessageParamUnion, error) {
	if m.Role == lipapi.RoleSystem {
		if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText {
			return openai.SystemMessage(m.Parts[0].Text), nil
		}
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: system message must be plain text for this adapter")
	}

	if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText {
		return openai.UserMessage(m.Parts[0].Text), nil
	}
	parts, err := userPartsToContentUnion(m.Parts)
	if err != nil {
		return openai.ChatCompletionMessageParamUnion{}, err
	}
	return openai.UserMessage(parts), nil
}

func userPartsToContentUnion(parts []lipapi.Part) ([]openai.ChatCompletionContentPartUnionParam, error) {
	out := make([]openai.ChatCompletionContentPartUnionParam, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, openai.TextContentPart(p.Text))
		case lipapi.PartImageRef:
			out = append(out, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: p.ImageRef,
			}))
		case lipapi.PartFileRef:
			b64, fname, err := fileDataFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
				FileData: openai.String(b64),
				Filename: openai.String(fname),
			}))
		default:
			return nil, fmt.Errorf("openailegacy: unsupported part kind %q in user message", p.Kind)
		}
	}
	return out, nil
}

func toolResultString(p lipapi.Part) string {
	if len(p.Content) == 0 {
		return ""
	}
	return string(p.Content)
}

func fileDataFromPart(p lipapi.Part) (dataB64, filename string, err error) {
	filename = strings.TrimSpace(p.FileName)
	ref := p.FileRef
	if strings.HasPrefix(ref, "data:") {
		_, b64, ok := stripDataURLBase64(ref)
		if !ok {
			return "", "", fmt.Errorf("openailegacy: invalid data URL in file part")
		}
		return b64, filename, nil
	}
	return ref, filename, nil
}

func stripDataURLBase64(dataURL string) (mime, b64 string, ok bool) {
	if !strings.HasPrefix(dataURL, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(dataURL, "data:")
	semi := strings.Index(rest, ";")
	if semi < 0 {
		return "", "", false
	}
	mime = rest[:semi]
	enc := rest[semi+1:]
	const prefix = "base64,"
	if !strings.HasPrefix(enc, prefix) {
		return "", "", false
	}
	return mime, enc[len(prefix):], true
}

func buildChatTools(tools []lipapi.ToolDef) ([]openai.ChatCompletionToolUnionParam, error) {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("openailegacy: tool %q parameters: %w", t.Name, err)
			}
		}
		if schema == nil {
			schema = map[string]any{}
		}
		fn := shared.FunctionDefinitionParam{
			Name:       t.Name,
			Parameters: schema,
			Strict:     param.NewOpt(true),
		}
		if strings.TrimSpace(t.Description) != "" {
			fn.Description = param.NewOpt(t.Description)
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out, nil
}

func chatToolChoiceUnion(tc lipapi.ToolChoice, nTools int) openai.ChatCompletionToolChoiceOptionUnionParam {
	mode := tc.Mode
	if mode == "" {
		mode = lipapi.ToolChoiceAuto
	}
	switch mode {
	case lipapi.ToolChoiceAuto:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("auto")}
	case lipapi.ToolChoiceNone:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("none")}
	case lipapi.ToolChoiceAny:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("required")}
	case lipapi.ToolChoiceRequired:
		if tc.Name != "" && nTools > 0 {
			return openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{
				Name: tc.Name,
			})
		}
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("required")}
	default:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("auto")}
	}
}
