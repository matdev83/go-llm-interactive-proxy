package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Extension key for wire model stored by the Anthropic frontend decoder.
const extModelJSONKey = "anthropic.model"

const defaultMaxTokens int64 = 4096

func newSDKClientForSecret(cfg Config, apiSecret string) anthropic.Client {
	opts := make([]option.RequestOption, 0, 3)
	opts = append(opts,
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(apiSecret),
	)
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	if cfg.SDKMaxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*cfg.SDKMaxRetries))
	}
	return anthropic.NewClient(opts...)
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

// ParamsForCall builds an Anthropic message create payload from a canonical call.
func ParamsForCall(call *lipapi.Call, cand routing.AttemptCandidate) (anthropic.MessageNewParams, error) {
	if call == nil {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: nil call")
	}
	model := resolveModel(cand, *call)
	if model == "" {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: model is required (route candidate or %s extension)", extModelJSONKey)
	}

	maxTok := defaultMaxTokens
	if call.Options.MaxOutputTokens != nil {
		maxTok = int64(*call.Options.MaxOutputTokens)
	}

	msgs, err := buildAnthropicMessages(call)
	if err != nil {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: build messages: %w", err)
	}

	p := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTok,
		Messages:  msgs,
	}

	if sys := buildSystemBlocks(call); len(sys) > 0 {
		p.System = sys
	}

	if len(call.Tools) > 0 {
		tools, err := buildTools(call.Tools)
		if err != nil {
			return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: build tools: %w", err)
		}
		p.Tools = tools
		p.ToolChoice = toolChoiceUnion(call, len(call.Tools))
	}

	o := call.Options
	if o.Temperature != nil {
		p.Temperature = param.NewOpt(*o.Temperature)
	}
	if o.TopP != nil {
		p.TopP = param.NewOpt(*o.TopP)
	}

	return p, nil
}

func buildSystemBlocks(call *lipapi.Call) []anthropic.TextBlockParam {
	capBlocks := 0
	if len(call.Instructions) > 0 {
		capBlocks++
	}
	for _, m := range call.Messages {
		if m.Role == lipapi.RoleSystem {
			capBlocks += len(m.Parts)
		}
	}

	var out []anthropic.TextBlockParam
	if capBlocks > 0 {
		out = make([]anthropic.TextBlockParam, 0, capBlocks)
	}

	if t := lipapi.JoinInstructionText(call.Instructions); t != "" {
		out = append(out, anthropic.TextBlockParam{Text: t})
	}
	for _, m := range call.Messages {
		if m.Role != lipapi.RoleSystem {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, anthropic.TextBlockParam{Text: p.Text})
		}
	}
	return out
}

func buildAnthropicMessages(call *lipapi.Call) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role == lipapi.RoleSystem {
			continue
		}
		u, err := messageToParam(m)
		if err != nil {
			return nil, fmt.Errorf("anthropic: message param: %w", err)
		}
		out = append(out, u)
	}
	return out, nil
}

func messageToParam(m lipapi.Message) (anthropic.MessageParam, error) {
	switch m.Role {
	case lipapi.RoleUser:
		return userMessageParam(m)
	case lipapi.RoleAssistant:
		return assistantMessageParam(m)
	case lipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != lipapi.PartToolResult {
			return anthropic.MessageParam{}, fmt.Errorf("anthropic: tool message must have one tool_result part")
		}
		p := m.Parts[0]
		content := string(p.Content)
		b := anthropic.NewToolResultBlock(p.ToolCallID, content, false)
		return anthropic.NewUserMessage(b), nil
	default:
		return anthropic.MessageParam{}, fmt.Errorf("anthropic: unsupported message role %q", m.Role)
	}
}

func userMessageParam(m lipapi.Message) (anthropic.MessageParam, error) {
	if len(m.Parts) == 1 && m.Parts[0].Kind == lipapi.PartText && strings.TrimSpace(m.Parts[0].Text) != "" {
		return anthropic.NewUserMessage(anthropic.NewTextBlock(m.Parts[0].Text)), nil
	}
	blocks, err := userPartsToBlocks(m.Parts)
	if err != nil {
		return anthropic.MessageParam{}, fmt.Errorf("anthropic: user parts: %w", err)
	}
	return anthropic.NewUserMessage(blocks...), nil
}

func assistantMessageParam(m lipapi.Message) (anthropic.MessageParam, error) {
	for _, p := range m.Parts {
		if p.Kind != lipapi.PartText {
			return anthropic.MessageParam{}, fmt.Errorf("anthropic: assistant message may only contain text parts in this adapter")
		}
	}
	var blocks []anthropic.ContentBlockParamUnion
	for _, p := range m.Parts {
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		blocks = append(blocks, anthropic.NewTextBlock(p.Text))
	}
	if len(blocks) == 0 {
		return anthropic.MessageParam{}, fmt.Errorf("anthropic: assistant message is empty after trimming")
	}
	return anthropic.NewAssistantMessage(blocks...), nil
}

func userPartsToBlocks(parts []lipapi.Part) ([]anthropic.ContentBlockParamUnion, error) {
	var out []anthropic.ContentBlockParamUnion
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, anthropic.NewTextBlock(p.Text))
		case lipapi.PartImageRef:
			blk, err := imageBlockFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, blk)
		case lipapi.PartFileRef:
			blk, err := documentBlockFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, blk)
		default:
			return nil, fmt.Errorf("anthropic: unsupported part kind %q in user message", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("anthropic: user message has no mappable content blocks")
	}
	return out, nil
}

func imageBlockFromPart(p lipapi.Part) (anthropic.ContentBlockParamUnion, error) {
	ref := p.ImageRef
	if strings.HasPrefix(ref, "data:") {
		mime, b64, ok := lipapi.StripDataURLBase64(ref)
		if !ok {
			return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: invalid data URL in image part")
		}
		_ = mime
		return anthropic.NewImageBlock(anthropic.Base64ImageSourceParam{
			Data:      b64,
			MediaType: anthropic.Base64ImageSourceMediaType(pickImageMediaType(mime, p.ImageMIME)),
		}), nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: ref}), nil
	}
	return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: imageRef must be a data URL or http(s) URL, got %q", ref)
}

func pickImageMediaType(fromDataURL, fromPart string) string {
	if s := strings.TrimSpace(fromPart); s != "" {
		return s
	}
	return fromDataURL
}

func documentBlockFromPart(p lipapi.Part) (anthropic.ContentBlockParamUnion, error) {
	ref := p.FileRef
	if !strings.HasPrefix(ref, "data:") {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: file part requires a data URL, got %q", ref)
	}
	mime, b64, ok := lipapi.StripDataURLBase64(ref)
	if !ok {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: invalid data URL in file part")
	}
	_ = mime
	// v1: PDF and similar via base64 application/pdf
	return anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
		Data: b64,
	}), nil
}

func buildTools(tools []lipapi.ToolDef) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema, err := toolInputSchema(t)
		if err != nil {
			return nil, err
		}
		tool := anthropic.ToolParam{
			Name:        t.Name,
			InputSchema: schema,
		}
		if strings.TrimSpace(t.Description) != "" {
			tool.Description = param.NewOpt(t.Description)
		}
		tool.Strict = anthropic.Bool(true)
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

func toolInputSchema(t lipapi.ToolDef) (anthropic.ToolInputSchemaParam, error) {
	if len(t.Parameters) == 0 {
		return anthropic.ToolInputSchemaParam{
			Type:       "object",
			Properties: map[string]any{},
		}, nil
	}
	var s anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(t.Parameters, &s); err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("anthropic: tool %q parameters: %w", t.Name, err)
	}
	return s, nil
}

func toolChoiceUnion(call *lipapi.Call, nTools int) anthropic.ToolChoiceUnionParam {
	if nTools == 0 {
		return anthropic.ToolChoiceUnionParam{}
	}
	dpar := disableParallelToolUseOpt(call)
	mode := call.ToolChoice.Mode
	if mode == "" {
		mode = lipapi.ToolChoiceAuto
	}
	switch mode {
	case lipapi.ToolChoiceNone:
		n := anthropic.NewToolChoiceNoneParam()
		return anthropic.ToolChoiceUnionParam{OfNone: &n}
	case lipapi.ToolChoiceAny:
		a := anthropic.ToolChoiceAnyParam{DisableParallelToolUse: dpar}
		return anthropic.ToolChoiceUnionParam{OfAny: &a}
	case lipapi.ToolChoiceRequired:
		if call.ToolChoice.Name != "" {
			t := anthropic.ToolChoiceToolParam{
				Name:                   call.ToolChoice.Name,
				DisableParallelToolUse: dpar,
			}
			return anthropic.ToolChoiceUnionParam{OfTool: &t}
		}
		a := anthropic.ToolChoiceAnyParam{DisableParallelToolUse: dpar}
		return anthropic.ToolChoiceUnionParam{OfAny: &a}
	case lipapi.ToolChoiceAuto, "":
		fallthrough
	default:
		au := anthropic.ToolChoiceAutoParam{DisableParallelToolUse: dpar}
		return anthropic.ToolChoiceUnionParam{OfAuto: &au}
	}
}

// When ParallelToolCalls is false, set Anthropic disable_parallel_tool_use to true.
func disableParallelToolUseOpt(call *lipapi.Call) param.Opt[bool] {
	if call == nil || call.Options.ParallelToolCalls == nil {
		return param.Opt[bool]{}
	}
	if *call.Options.ParallelToolCalls {
		return param.Opt[bool]{}
	}
	return param.NewOpt(true)
}
