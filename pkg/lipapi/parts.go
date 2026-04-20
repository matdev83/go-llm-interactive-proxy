package lipapi

import (
	"encoding/json"
	"errors"
	"fmt"
)

// PartKind classifies canonical content parts.
type PartKind string

const (
	PartText       PartKind = "text"
	PartImageRef   PartKind = "image_ref"
	PartFileRef    PartKind = "file_ref"
	PartToolResult PartKind = "tool_result"
	PartJSON       PartKind = "json"
)

// TextPart constructs a validated text part for tests and adapters.
func TextPart(s string) Part {
	return Part{Kind: PartText, Text: s}
}

// FilePart constructs a file reference part for documents, PDFs, and other binary attachments.
func FilePart(ref, mime, name string) Part {
	return Part{Kind: PartFileRef, FileRef: ref, FileMIME: mime, FileName: name}
}

// Part is one ordered content fragment inside a message.
type Part struct {
	Kind PartKind

	Text string

	ImageRef  string
	ImageMIME string

	FileRef  string
	FileMIME string
	FileName string

	ToolCallID string
	ToolName   string
	Content    json.RawMessage
}

func (p Part) validate() error {
	switch p.Kind {
	case PartText:
		if p.Text == "" {
			return errors.New("text part requires non-empty Text")
		}
	case PartImageRef:
		if p.ImageRef == "" {
			return errors.New("image_ref part requires ImageRef")
		}
	case PartFileRef:
		if p.FileRef == "" {
			return errors.New("file_ref part requires FileRef")
		}
	case PartToolResult:
		if p.ToolCallID == "" {
			return errors.New("tool_result part requires ToolCallID")
		}
	case PartJSON:
		if len(p.Content) == 0 {
			return errors.New("json part requires Content")
		}
		if !json.Valid(p.Content) {
			return errors.New("json part Content must be valid JSON")
		}
	case "":
		return errors.New("part kind is required")
	default:
		return fmt.Errorf("unknown part kind %q", p.Kind)
	}
	return nil
}

// ToolDef is a canonical function/tool declaration (not a raw provider blob).
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolChoiceMode selects how tool calls are admitted for this request.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceAny      ToolChoiceMode = "any"
	ToolChoiceRequired ToolChoiceMode = "required"
)

// ToolChoice constrains tool usage for the call.
type ToolChoice struct {
	Mode ToolChoiceMode
	// Name is used when Mode requires a specific tool.
	Name string
}

func (tc ToolChoice) validate(toolCount int, tools []ToolDef) error {
	switch tc.Mode {
	case "", ToolChoiceAuto, ToolChoiceAny, ToolChoiceNone, ToolChoiceRequired:
	default:
		return &ValidationError{Field: "ToolChoice.Mode", Message: fmt.Sprintf("unknown tool choice mode %q", tc.Mode)}
	}
	mode := tc.Mode
	if mode == "" {
		mode = ToolChoiceAuto
	}
	if mode == ToolChoiceNone && toolCount > 0 {
		return &ValidationError{Field: "ToolChoice", Message: "ToolChoiceNone is incompatible with declared tools"}
	}
	if mode == ToolChoiceRequired {
		if toolCount == 0 {
			return &ValidationError{Field: "Tools", Message: "ToolChoiceRequired requires at least one tool definition"}
		}
		if tc.Name == "" {
			return &ValidationError{Field: "ToolChoice.Name", Message: "ToolChoiceRequired requires ToolChoice.Name"}
		}
		found := false
		for _, t := range tools {
			if t.Name == tc.Name {
				found = true
				break
			}
		}
		if !found {
			return &ValidationError{Field: "ToolChoice.Name", Message: "ToolChoiceRequired name must match a declared tool"}
		}
	}
	return nil
}

// GenerationOptions captures cross-protocol generation controls.
type GenerationOptions struct {
	Temperature       *float64
	MaxOutputTokens   *int
	TopP              *float64
	ReasoningEffort   string
	ResponseMIMEType  string
	ParallelToolCalls *bool
}

func (o GenerationOptions) validate() error {
	if o.Temperature != nil {
		t := *o.Temperature
		if t < 0 || t > 2 {
			return &ValidationError{Field: "Options.Temperature", Message: "temperature must be between 0 and 2"}
		}
	}
	if o.TopP != nil {
		p := *o.TopP
		if p < 0 || p > 1 {
			return &ValidationError{Field: "Options.TopP", Message: "top_p must be between 0 and 1"}
		}
	}
	if o.MaxOutputTokens != nil && *o.MaxOutputTokens < 0 {
		return &ValidationError{Field: "Options.MaxOutputTokens", Message: "max_output_tokens must be non-negative"}
	}
	return nil
}
