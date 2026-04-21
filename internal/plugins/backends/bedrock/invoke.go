package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Extension key for wire model id stored by a frontend decoder.
const extModelJSONKey = "bedrock.modelId"

func newRuntimeClient(cfg Config) (*bedrockruntime.Client, error) {
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: aws config: %w", err)
	}
	opts := []func(*bedrockruntime.Options){}
	if u := strings.TrimSpace(cfg.BaseEndpoint); u != "" {
		opts = append(opts, func(o *bedrockruntime.Options) {
			o.BaseEndpoint = aws.String(u)
			if cfg.DisableHTTPS {
				o.EndpointOptions.DisableHTTPS = true
			}
		})
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, func(o *bedrockruntime.Options) {
			o.HTTPClient = cfg.HTTPClient
		})
	}
	return bedrockruntime.NewFromConfig(awsCfg, opts...), nil
}

func resolveModelID(cand routing.AttemptCandidate, call lipapi.Call) string {
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

// ConverseStreamInputForCall maps a canonical call to Bedrock ConverseStream input.
func ConverseStreamInputForCall(call *lipapi.Call, cand routing.AttemptCandidate) (*bedrockruntime.ConverseStreamInput, error) {
	if call == nil {
		return nil, fmt.Errorf("bedrock: nil call")
	}
	modelID := resolveModelID(cand, *call)
	if modelID == "" {
		return nil, fmt.Errorf("bedrock: model id is required (route candidate or %s extension)", extModelJSONKey)
	}

	sys, err := buildSystemBlocks(call)
	if err != nil {
		return nil, err
	}
	msgs, err := buildMessages(call)
	if err != nil {
		return nil, err
	}

	in := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(modelID),
		Messages: msgs,
	}
	if len(sys) > 0 {
		in.System = sys
	}

	if o := call.Options; o.Temperature != nil || o.TopP != nil || o.MaxOutputTokens != nil {
		ic := &types.InferenceConfiguration{}
		if o.MaxOutputTokens != nil {
			ic.MaxTokens = aws.Int32(int32(*o.MaxOutputTokens))
		}
		if o.Temperature != nil {
			ic.Temperature = aws.Float32(float32(*o.Temperature))
		}
		if o.TopP != nil {
			ic.TopP = aws.Float32(float32(*o.TopP))
		}
		in.InferenceConfig = ic
	}

	if len(call.Tools) > 0 {
		tc, err := buildToolConfig(call)
		if err != nil {
			return nil, err
		}
		in.ToolConfig = tc
	}

	return in, nil
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

func buildSystemBlocks(call *lipapi.Call) ([]types.SystemContentBlock, error) {
	var out []types.SystemContentBlock
	if t := joinInstructionText(call.Instructions); t != "" {
		out = append(out, &types.SystemContentBlockMemberText{Value: t})
	}
	for _, m := range call.Messages {
		if m.Role != lipapi.RoleSystem {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != lipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, &types.SystemContentBlockMemberText{Value: p.Text})
		}
	}
	return out, nil
}

func buildMessages(call *lipapi.Call) ([]types.Message, error) {
	var out []types.Message
	for _, m := range call.Messages {
		if m.Role == lipapi.RoleSystem {
			continue
		}
		msg, err := lipMessageToBedrock(m)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bedrock: no non-system messages")
	}
	return out, nil
}

func lipMessageToBedrock(m lipapi.Message) (types.Message, error) {
	switch m.Role {
	case lipapi.RoleUser:
		blocks, err := userPartsToContentBlocks(m.Parts)
		if err != nil {
			return types.Message{}, err
		}
		return types.Message{
			Role:    types.ConversationRoleUser,
			Content: blocks,
		}, nil
	case lipapi.RoleAssistant:
		blocks, err := assistantPartsToContentBlocks(m.Parts)
		if err != nil {
			return types.Message{}, err
		}
		return types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: blocks,
		}, nil
	case lipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != lipapi.PartToolResult {
			return types.Message{}, fmt.Errorf("bedrock: tool message must have one tool_result part")
		}
		p := m.Parts[0]
		tb := types.ToolResultBlock{
			ToolUseId: aws.String(p.ToolCallID),
			Content: []types.ToolResultContentBlock{
				&types.ToolResultContentBlockMemberText{Value: string(p.Content)},
			},
			Status: types.ToolResultStatusSuccess,
		}
		return types.Message{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberToolResult{Value: tb},
			},
		}, nil
	default:
		return types.Message{}, fmt.Errorf("bedrock: unsupported message role %q", m.Role)
	}
}

func assistantPartsToContentBlocks(parts []lipapi.Part) ([]types.ContentBlock, error) {
	var out []types.ContentBlock
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, &types.ContentBlockMemberText{Value: p.Text})
		case lipapi.PartJSON:
			var payload map[string]any
			if err := json.Unmarshal(p.Content, &payload); err != nil {
				return nil, fmt.Errorf("bedrock: assistant json part: %w", err)
			}
			toolUseID, _ := payload["tool_use_id"].(string)
			toolName, _ := payload["name"].(string)
			if toolUseID == "" || toolName == "" {
				return nil, fmt.Errorf("bedrock: assistant json part requires tool_use_id and name")
			}
			input := payload["input"]
			if input == nil {
				input = map[string]any{}
			}
			out = append(out, &types.ContentBlockMemberToolUse{
				Value: types.ToolUseBlock{
					ToolUseId: aws.String(toolUseID),
					Name:      aws.String(toolName),
					Input:     bedrockdoc.NewLazyDocument(input),
				},
			})
		default:
			return nil, fmt.Errorf("bedrock: unsupported assistant part kind %q", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bedrock: assistant message is empty after mapping")
	}
	return out, nil
}

func userPartsToContentBlocks(parts []lipapi.Part) ([]types.ContentBlock, error) {
	var out []types.ContentBlock
	for _, p := range parts {
		switch p.Kind {
		case lipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, &types.ContentBlockMemberText{Value: p.Text})
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
			return nil, fmt.Errorf("bedrock: unsupported part kind %q in user message", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bedrock: user message has no mappable content blocks")
	}
	return out, nil
}

func imageBlockFromPart(p lipapi.Part) (types.ContentBlock, error) {
	ref := p.ImageRef
	if strings.HasPrefix(ref, "data:") {
		mime, b64, ok := stripDataURLBase64(ref)
		if !ok {
			return nil, fmt.Errorf("bedrock: invalid data URL in image part")
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bedrock: image base64: %w", err)
		}
		fmt := imageFormatFromMIME(pickImageMediaType(mime, p.ImageMIME))
		return &types.ContentBlockMemberImage{
			Value: types.ImageBlock{
				Format: fmt,
				Source: &types.ImageSourceMemberBytes{Value: raw},
			},
		}, nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return nil, fmt.Errorf("bedrock: image URL sources are not supported for Converse in this adapter; use a data URL")
	}
	return nil, fmt.Errorf("bedrock: imageRef must be a data URL, got %q", ref)
}

func documentBlockFromPart(p lipapi.Part) (types.ContentBlock, error) {
	ref := p.FileRef
	if !strings.HasPrefix(ref, "data:") {
		return nil, fmt.Errorf("bedrock: file part requires a data URL, got %q", ref)
	}
	mime, b64, ok := stripDataURLBase64(ref)
	if !ok {
		return nil, fmt.Errorf("bedrock: invalid data URL in file part")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("bedrock: document base64: %w", err)
	}
	_ = mime
	name := strings.TrimSpace(p.FileName)
	if name == "" {
		name = "document.pdf"
	}
	return &types.ContentBlockMemberDocument{
		Value: types.DocumentBlock{
			Name:   aws.String(name),
			Format: types.DocumentFormatPdf,
			Source: &types.DocumentSourceMemberBytes{Value: raw},
		},
	}, nil
}

func pickImageMediaType(fromDataURL, fromPart string) string {
	if s := strings.TrimSpace(fromPart); s != "" {
		return s
	}
	return fromDataURL
}

func imageFormatFromMIME(mime string) types.ImageFormat {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return types.ImageFormatJpeg
	case "image/webp":
		return types.ImageFormatWebp
	case "image/gif":
		return types.ImageFormatGif
	default:
		return types.ImageFormatPng
	}
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

func buildToolConfig(call *lipapi.Call) (*types.ToolConfiguration, error) {
	tools := make([]types.Tool, 0, len(call.Tools))
	for _, t := range call.Tools {
		var schema any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("bedrock: tool %q parameters: %w", t.Name, err)
			}
		} else {
			schema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		spec := types.ToolSpecification{
			Name: aws.String(t.Name),
			InputSchema: &types.ToolInputSchemaMemberJson{
				Value: bedrockdoc.NewLazyDocument(schema),
			},
		}
		if strings.TrimSpace(t.Description) != "" {
			spec.Description = aws.String(t.Description)
		}
		tools = append(tools, &types.ToolMemberToolSpec{Value: spec})
	}
	cfg := &types.ToolConfiguration{Tools: tools}
	mode := call.ToolChoice.Mode
	if mode == "" {
		mode = lipapi.ToolChoiceAuto
	}
	switch mode {
	case lipapi.ToolChoiceAuto:
		cfg.ToolChoice = &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
	case lipapi.ToolChoiceAny:
		cfg.ToolChoice = &types.ToolChoiceMemberAny{Value: types.AnyToolChoice{}}
	case lipapi.ToolChoiceRequired:
		cfg.ToolChoice = &types.ToolChoiceMemberTool{
			Value: types.SpecificToolChoice{Name: aws.String(call.ToolChoice.Name)},
		}
	case lipapi.ToolChoiceNone:
		// Call.Validate rejects tools+none; if we ever get here, omit tool config.
		return nil, fmt.Errorf("bedrock: ToolChoiceNone with tools is invalid")
	default:
		cfg.ToolChoice = &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
	}
	return cfg, nil
}

// Config configures the Bedrock Runtime ConverseStream connector (AWS SDK v2).
type Config struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// BaseEndpoint is optional (e.g. httptest server URL for integration tests).
	BaseEndpoint string
	// DisableHTTPS must be true when BaseEndpoint is http:// (emulator).
	DisableHTTPS bool
	HTTPClient   *http.Client
}
