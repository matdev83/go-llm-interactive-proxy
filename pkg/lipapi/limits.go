package lipapi

import (
	"fmt"
)

// Envelope size limits for Call validation. They bound how much work a single
// request can force in the core and protect against pathological or hostile
// clients. Frontends also cap raw HTTP body size; these apply to the decoded
// canonical call.
const (
	MaxCallIDBytes          = 512
	MaxRouteSelectorBytes   = 64 * 1024
	MaxClientSessionIDBytes = 4 * 1024
	MaxContinuityKeyBytes   = 4 * 1024
	MaxALegIDBytes          = 4 * 1024
	MaxMessages             = 4_096
	MaxInstructionMessages  = 1_024
	MaxPartsPerMessage      = 2_048
	MaxTools                = 2_048
	MaxToolNameBytes        = 256
	MaxToolDescriptionBytes = 32 * 1024
	MaxToolParametersBytes  = 256 * 1024
	MaxExtensionKeys        = 256
	MaxExtensionKeyBytes    = 256
	MaxExtensionValueBytes  = 4 * 1024 * 1024
	// Part content: align with typical HTTP request body caps (8 MiB) for text/JSON;
	// ref fields are path/URL length limits.
	MaxPartTextBytes  = 8 * 1024 * 1024
	MaxPartJSONBytes  = 8 * 1024 * 1024
	MaxRefStringBytes = 8 * 1024

	// Canonical streaming Event field caps (adapter/hook mutations).
	MaxEventDeltaBytes       = MaxPartTextBytes  // TextDelta, ReasoningDelta, ToolCallArgsDelta
	MaxEventDiagMessageBytes = 4 << 20           // WarningMessage, ErrorMessage per event
	MaxEventCodeFieldBytes   = MaxRefStringBytes // WarningCode, ErrorCode when non-empty
)

func validateStringField(name, s string, max int) error {
	if max <= 0 {
		return nil
	}
	if len(s) > max {
		return &ValidationError{Field: name, Message: fmt.Sprintf("exceeds %d bytes", max)}
	}
	return nil
}

func (c Call) validateEnvelopeSizes() error {
	if err := validateStringField("ID", c.ID, MaxCallIDBytes); err != nil {
		return err
	}
	if err := validateStringField("Route.Selector", c.Route.Selector, MaxRouteSelectorBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ClientSessionID", c.Session.ClientSessionID, MaxClientSessionIDBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ContinuityKey", c.Session.ContinuityKey, MaxContinuityKeyBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ALegID", c.Session.ALegID, MaxALegIDBytes); err != nil {
		return err
	}
	if len(c.Messages) > MaxMessages {
		return &ValidationError{Field: "Messages", Message: fmt.Sprintf("at most %d messages", MaxMessages)}
	}
	if len(c.Instructions) > MaxInstructionMessages {
		return &ValidationError{Field: "Instructions", Message: fmt.Sprintf("at most %d instruction messages", MaxInstructionMessages)}
	}
	if len(c.Tools) > MaxTools {
		return &ValidationError{Field: "Tools", Message: fmt.Sprintf("at most %d tools", MaxTools)}
	}
	if len(c.Extensions) > MaxExtensionKeys {
		return &ValidationError{Field: "Extensions", Message: fmt.Sprintf("at most %d extension entries", MaxExtensionKeys)}
	}
	for k, v := range c.Extensions {
		if err := validateStringField(fmt.Sprintf("Extensions[%q]", k), k, MaxExtensionKeyBytes); err != nil {
			return err
		}
		if len(v) > MaxExtensionValueBytes {
			return &ValidationError{Field: fmt.Sprintf("Extensions[%q]", k), Message: fmt.Sprintf("extension value exceeds %d bytes", MaxExtensionValueBytes)}
		}
	}
	for i, m := range c.Messages {
		if len(m.Parts) > MaxPartsPerMessage {
			return &ValidationError{Field: fmt.Sprintf("Messages[%d].Parts", i), Message: fmt.Sprintf("at most %d parts per message", MaxPartsPerMessage)}
		}
		for j, p := range m.Parts {
			if err := validatePartSizes(fmt.Sprintf("Messages[%d].Parts[%d]", i, j), p); err != nil {
				return err
			}
		}
	}
	for i, m := range c.Instructions {
		if len(m.Parts) > MaxPartsPerMessage {
			return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Parts", i), Message: fmt.Sprintf("at most %d parts per message", MaxPartsPerMessage)}
		}
		for j, p := range m.Parts {
			if err := validatePartSizes(fmt.Sprintf("Instructions[%d].Parts[%d]", i, j), p); err != nil {
				return err
			}
		}
	}
	for i, t := range c.Tools {
		if err := validateStringField(fmt.Sprintf("Tools[%d].Name", i), t.Name, MaxToolNameBytes); err != nil {
			return err
		}
		if err := validateStringField(fmt.Sprintf("Tools[%d].Description", i), t.Description, MaxToolDescriptionBytes); err != nil {
			return err
		}
		if len(t.Parameters) > MaxToolParametersBytes {
			return &ValidationError{Field: fmt.Sprintf("Tools[%d].Parameters", i), Message: fmt.Sprintf("exceeds %d bytes", MaxToolParametersBytes)}
		}
	}
	return nil
}

func validatePartSizes(field string, p Part) error {
	switch p.Kind {
	case PartText:
		if len(p.Text) > MaxPartTextBytes {
			return &ValidationError{Field: field + ".Text", Message: fmt.Sprintf("text exceeds %d bytes", MaxPartTextBytes)}
		}
	case PartImageRef:
		if err := validateStringField(field+".ImageRef", p.ImageRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".ImageMIME", p.ImageMIME, MaxRefStringBytes); err != nil {
			return err
		}
	case PartFileRef:
		if err := validateStringField(field+".FileRef", p.FileRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".FileMIME", p.FileMIME, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".FileName", p.FileName, MaxRefStringBytes); err != nil {
			return err
		}
	case PartToolResult:
		if err := validateStringField(field+".ToolCallID", p.ToolCallID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".ToolName", p.ToolName, MaxRefStringBytes); err != nil {
			return err
		}
		if len(p.Content) > MaxPartJSONBytes {
			return &ValidationError{Field: field + ".Content", Message: fmt.Sprintf("content exceeds %d bytes", MaxPartJSONBytes)}
		}
	case PartJSON:
		if len(p.Content) > MaxPartJSONBytes {
			return &ValidationError{Field: field + ".Content", Message: fmt.Sprintf("content exceeds %d bytes", MaxPartJSONBytes)}
		}
	case "":
		// validated in validate()
	default:
	}
	if p.ToolCallID != "" && len(p.ToolCallID) > MaxRefStringBytes {
		return &ValidationError{Field: field + ".ToolCallID", Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	if p.ToolName != "" && len(p.ToolName) > MaxRefStringBytes {
		return &ValidationError{Field: field + ".ToolName", Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	return nil
}

// ReasoningEffort / MIME strings and similar option strings.
const MaxOptionStringBytes = 4 * 1024

func validateOptionStrings(o GenerationOptions) error {
	if o.ReasoningEffort != "" && len(o.ReasoningEffort) > MaxOptionStringBytes {
		return &ValidationError{Field: "Options.ReasoningEffort", Message: fmt.Sprintf("exceeds %d bytes", MaxOptionStringBytes)}
	}
	if o.ResponseMIMEType != "" && len(o.ResponseMIMEType) > MaxOptionStringBytes {
		return &ValidationError{Field: "Options.ResponseMIMEType", Message: fmt.Sprintf("exceeds %d bytes", MaxOptionStringBytes)}
	}
	return nil
}
