package lipapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ItemKind classifies ordered canonical item types.
type ItemKind string

const (
	ItemKindMessage       ItemKind = "message"
	ItemKindItemReference ItemKind = "item_reference"
	ItemKindToolCall      ItemKind = "tool_call"
	ItemKindToolResult    ItemKind = "tool_result"
	ItemKindReasoning     ItemKind = "reasoning"
	ItemKindCompaction    ItemKind = "compaction"
	ItemKindExtension     ItemKind = "extension"
)

// ItemStatus identifies the lifecycle status of an item.
type ItemStatus string

const (
	ItemStatusInProgress ItemStatus = "in_progress"
	ItemStatusCompleted  ItemStatus = "completed"
	ItemStatusIncomplete ItemStatus = "incomplete"
)

// AssistantPhase identifies assistant output phase (commentary vs final answer).
type AssistantPhase string

const (
	AssistantPhaseCommentary  AssistantPhase = "commentary"
	AssistantPhaseFinalAnswer AssistantPhase = "final_answer"
)

// ContentPartKind classifies canonical content part forms inside items.
type ContentPartKind string

const (
	ContentPartText         ContentPartKind = "text"
	ContentPartImageRef     ContentPartKind = "image_ref"
	ContentPartFileRef      ContentPartKind = "file_ref"
	ContentPartVideoRef     ContentPartKind = "video_ref"
	ContentPartRefusal      ContentPartKind = "refusal"
	ContentPartReasoning    ContentPartKind = "reasoning"
	ContentPartSummary      ContentPartKind = "summary"
	ContentPartAnnotation   ContentPartKind = "annotation"
	ContentPartAssistantRef ContentPartKind = "assistant_ref"
	ContentPartJSON         ContentPartKind = "json"
	ContentPartToolResult   ContentPartKind = "tool_result"
	// ContentPartExtension is an opaque vendor-prefixed custom content part that
	// the canonical model cannot interpret but must preserve losslessly. Its
	// structured payload is carried as raw JSON (never stringified to text).
	ContentPartExtension ContentPartKind = "extension"
)

// AnnotationPart holds annotation metadata for text/content.
type AnnotationPart struct {
	Type string          `json:"type,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ExtensionContentPart carries a vendor-prefixed custom content part that the
// canonical model cannot interpret but must preserve losslessly. Type is the
// prefixed wire discriminator (for example "acme:input_file" or
// "acme.com/part"); Data is the full raw wire part object (including its type
// field) so encoding can emit it verbatim without stringifying the structured
// payload.
type ExtensionContentPart struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

func (e *ExtensionContentPart) validate(field string) error {
	if e == nil {
		return &ValidationError{Field: field, Message: "extension content part is nil"}
	}
	if strings.TrimSpace(e.Type) == "" {
		return &ValidationError{Field: field + ".Type", Message: "extension type is required"}
	}
	if !strings.ContainsAny(e.Type, ":/") {
		return &ValidationError{Field: field + ".Type", Message: "extension type must be vendor-prefixed (contains ':' or '/')"}
	}
	if e.Type != strings.TrimSpace(e.Type) {
		return &ValidationError{Field: field + ".Type", Message: "extension type must not contain leading or trailing whitespace"}
	}
	if err := validateStringField(field+".Type", e.Type, MaxExtensionTypeBytes); err != nil {
		return err
	}
	if len(e.Data) > MaxExtensionDataBytes {
		return &ValidationError{Field: field + ".Data", Message: fmt.Sprintf("extension data exceeds %d bytes", MaxExtensionDataBytes)}
	}
	if len(e.Data) == 0 {
		return &ValidationError{Field: field + ".Data", Message: "extension content part requires Data"}
	}
	if !json.Valid(e.Data) {
		return &ValidationError{Field: field + ".Data", Message: "extension data must be valid JSON"}
	}
	if err := validateJSONDepth(e.Data, MaxJSONDepth); err != nil {
		return &ValidationError{Field: field + ".Data", Message: err.Error()}
	}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(e.Data, &obj); err != nil {
		return &ValidationError{Field: field + ".Data", Message: "extension data must be a JSON object"}
	}
	if obj.Type != e.Type {
		return &ValidationError{Field: field + ".Data", Message: "extension data type field must match Type"}
	}
	return nil
}

// ContentPart is one ordered content fragment within a canonical item.
type ContentPart struct {
	Kind ContentPartKind `json:"kind"`

	Text string `json:"text,omitempty"`

	ImageRef  string `json:"image_ref,omitempty"`
	ImageMIME string `json:"image_mime,omitempty"`

	FileRef  string `json:"file_ref,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileMIME string `json:"file_mime,omitempty"`
	FileName string `json:"file_name,omitempty"`

	VideoRef  string `json:"video_ref,omitempty"`
	VideoMIME string `json:"video_mime,omitempty"`

	Refusal string `json:"refusal,omitempty"`

	Reasoning *ReasoningPart `json:"reasoning,omitempty"`

	Summary string `json:"summary,omitempty"`

	Annotation *AnnotationPart `json:"annotation,omitempty"`

	AssistantRef string `json:"assistant_ref,omitempty"`

	// Extension carries a vendor-prefixed custom content part preserved
	// opaquely when Kind is ContentPartExtension.
	Extension *ExtensionContentPart `json:"extension,omitempty"`
}

func (cp ContentPart) validate(field string) error {
	switch cp.Kind {
	case ContentPartText:
		if cp.Text == "" {
			return &ValidationError{Field: field + ".Text", Message: "text part requires non-empty Text"}
		}
		if len(cp.Text) > MaxPartTextBytes {
			return &ValidationError{Field: field + ".Text", Message: fmt.Sprintf("text exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartImageRef:
		if cp.ImageRef == "" {
			return &ValidationError{Field: field + ".ImageRef", Message: "image_ref part requires ImageRef"}
		}
		if err := validateStringField(field+".ImageRef", cp.ImageRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".ImageMIME", cp.ImageMIME, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartFileRef:
		if cp.FileRef == "" && cp.FileData == "" {
			return &ValidationError{Field: field + ".FileRef", Message: "file_ref part requires FileRef or FileData"}
		}
		if err := validateStringField(field+".FileRef", cp.FileRef, MaxRefStringBytes); err != nil {
			return err
		}
		if len(cp.FileData) > MaxFileDataBytes {
			return &ValidationError{Field: field + ".FileData", Message: fmt.Sprintf("file_data exceeds %d bytes", MaxFileDataBytes)}
		}
		if err := validateStringField(field+".FileMIME", cp.FileMIME, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".FileName", cp.FileName, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartVideoRef:
		if cp.VideoRef == "" {
			return &ValidationError{Field: field + ".VideoRef", Message: "video_ref part requires VideoRef"}
		}
		if err := validateStringField(field+".VideoRef", cp.VideoRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".VideoMIME", cp.VideoMIME, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartRefusal:
		if cp.Refusal == "" {
			return &ValidationError{Field: field + ".Refusal", Message: "refusal part requires non-empty Refusal"}
		}
		if len(cp.Refusal) > MaxPartTextBytes {
			return &ValidationError{Field: field + ".Refusal", Message: fmt.Sprintf("refusal exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartReasoning:
		if cp.Reasoning == nil {
			return &ValidationError{Field: field + ".Reasoning", Message: "reasoning part requires Reasoning"}
		}
		if err := validateReasoningPart(cp.Reasoning); err != nil {
			return &ValidationError{Field: field + ".Reasoning", Message: err.Error()}
		}
	case ContentPartSummary:
		if cp.Summary == "" {
			return &ValidationError{Field: field + ".Summary", Message: "summary part requires non-empty Summary"}
		}
		if len(cp.Summary) > MaxPartTextBytes {
			return &ValidationError{Field: field + ".Summary", Message: fmt.Sprintf("summary exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartAnnotation:
		if cp.Annotation == nil {
			return &ValidationError{Field: field + ".Annotation", Message: "annotation part requires Annotation"}
		}
		if err := validateStringField(field+".Annotation.Type", cp.Annotation.Type, MaxRefStringBytes); err != nil {
			return err
		}
		if len(cp.Annotation.Data) > MaxPartJSONBytes {
			return &ValidationError{Field: field + ".Annotation.Data", Message: fmt.Sprintf("annotation data exceeds %d bytes", MaxPartJSONBytes)}
		}
		if len(cp.Annotation.Data) > 0 {
			if !json.Valid(cp.Annotation.Data) {
				return &ValidationError{Field: field + ".Annotation.Data", Message: "annotation data must be valid JSON"}
			}
			if err := validateJSONDepth(cp.Annotation.Data, MaxJSONDepth); err != nil {
				return &ValidationError{Field: field + ".Annotation.Data", Message: err.Error()}
			}
		}
	case ContentPartAssistantRef:
		if cp.AssistantRef == "" {
			return &ValidationError{Field: field + ".AssistantRef", Message: "assistant_ref part requires AssistantRef"}
		}
		if err := validateStringField(field+".AssistantRef", cp.AssistantRef, MaxRefStringBytes); err != nil {
			return err
		}
	case ContentPartJSON:
		if cp.Text == "" {
			return &ValidationError{Field: field + ".Text", Message: "json part requires non-empty Text"}
		}
		if len(cp.Text) > MaxPartJSONBytes {
			return &ValidationError{Field: field + ".Text", Message: fmt.Sprintf("json exceeds %d bytes", MaxPartJSONBytes)}
		}
		if !json.Valid([]byte(cp.Text)) {
			return &ValidationError{Field: field + ".Text", Message: "json part must contain valid JSON text"}
		}
	case ContentPartToolResult:
		if cp.Text == "" {
			return &ValidationError{Field: field + ".Text", Message: "tool_result part requires non-empty Text"}
		}
		if len(cp.Text) > MaxPartTextBytes {
			return &ValidationError{Field: field + ".Text", Message: fmt.Sprintf("tool_result exceeds %d bytes", MaxPartTextBytes)}
		}
	case ContentPartExtension:
		if cp.Extension == nil {
			return &ValidationError{Field: field + ".Extension", Message: "extension part requires Extension"}
		}
		if err := cp.Extension.validate(field + ".Extension"); err != nil {
			return err
		}
	case "":
		return &ValidationError{Field: field + ".Kind", Message: "content part kind is required"}
	default:
		return &ValidationError{Field: field + ".Kind", Message: fmt.Sprintf("unknown content part kind %q", cp.Kind)}
	}
	return nil
}

// ItemReference references a previously produced or existing item by ID.
type ItemReference struct {
	ID string `json:"id"`
}

// ToolCallItem represents a function/tool invocation request in an item trajectory.
type ToolCallItem struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolResultItem represents function output in an item trajectory.
type ToolResultItem struct {
	CallID string        `json:"call_id"`
	Name   string        `json:"name"`
	Output string        `json:"output,omitempty"`
	Parts  []ContentPart `json:"parts,omitempty"`
}

// ReasoningItem encapsulates reasoning payload within an item.
type ReasoningItem struct {
	Reasoning *ReasoningPart `json:"reasoning,omitempty"`
}

// CompactionItem encapsulates a context compaction item within an item trajectory.
type CompactionItem struct {
	EncapsulatedID string `json:"encapsulated_id,omitempty"`
	Dialect        string `json:"dialect,omitempty"`
	Implementor    string `json:"implementor,omitempty"`
	// EncryptedContent carries the provider compaction blob (pinned profile
	// encrypted_content). It is protocol-neutral opaque data that the
	// OpenResponses wire encoder renders verbatim on response.compaction output.
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Opaque           json.RawMessage `json:"opaque,omitempty"`
}

// OpaqueExtension carries namespaced extensions attached to items or calls.
type OpaqueExtension struct {
	Namespace   string          `json:"namespace"`
	Type        string          `json:"type"`
	Implementor string          `json:"implementor,omitempty"`
	Direction   string          `json:"direction,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

func (e *OpaqueExtension) validate(field string) error {
	if e == nil {
		return &ValidationError{Field: field, Message: "extension record is nil"}
	}
	ns := strings.TrimSpace(e.Namespace)
	if ns == "" {
		return &ValidationError{Field: field + ".Namespace", Message: "extension namespace is required"}
	}
	if strings.ContainsAny(ns, " \t\r\n") {
		return &ValidationError{Field: field + ".Namespace", Message: "extension namespace must not contain whitespace"}
	}
	if err := validateStringField(field+".Namespace", e.Namespace, MaxExtensionNamespaceBytes); err != nil {
		return err
	}
	if strings.TrimSpace(e.Type) == "" {
		return &ValidationError{Field: field + ".Type", Message: "extension type is required"}
	}
	if err := validateStringField(field+".Type", e.Type, MaxExtensionTypeBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Implementor", e.Implementor, MaxExtensionImplementorBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Direction", e.Direction, MaxExtensionDirectionBytes); err != nil {
		return err
	}
	if len(e.Data) > MaxExtensionDataBytes {
		return &ValidationError{Field: field + ".Data", Message: fmt.Sprintf("extension data exceeds %d bytes", MaxExtensionDataBytes)}
	}
	if len(e.Data) > 0 {
		if !json.Valid(e.Data) {
			return &ValidationError{Field: field + ".Data", Message: "extension data must be valid JSON"}
		}
		if err := validateJSONDepth(e.Data, MaxJSONDepth); err != nil {
			return &ValidationError{Field: field + ".Data", Message: err.Error()}
		}
	}
	return nil
}

// Item is one canonical ordered item in a conversation trajectory.
type Item struct {
	Kind       ItemKind         `json:"kind"`
	ID         string           `json:"id,omitempty"`
	Status     ItemStatus       `json:"status,omitempty"`
	Role       Role             `json:"role,omitempty"`
	Phase      AssistantPhase   `json:"phase,omitempty"`
	Content    []ContentPart    `json:"content,omitempty"`
	Reference  *ItemReference   `json:"reference,omitempty"`
	ToolCall   *ToolCallItem    `json:"tool_call,omitempty"`
	ToolResult *ToolResultItem  `json:"tool_result,omitempty"`
	Reasoning  *ReasoningItem   `json:"reasoning,omitempty"`
	Compaction *CompactionItem  `json:"compaction,omitempty"`
	Extension  *OpaqueExtension `json:"extension,omitempty"`
}

func (item Item) validate(field string) error {
	if err := validateStringField(field+".ID", item.ID, MaxItemReferenceIDBytes); err != nil {
		return err
	}
	if item.Status != "" {
		switch item.Status {
		case ItemStatusInProgress, ItemStatusCompleted, ItemStatusIncomplete:
		default:
			return &ValidationError{Field: field + ".Status", Message: fmt.Sprintf("invalid item status %q", item.Status)}
		}
	}
	if item.Phase != "" {
		switch item.Phase {
		case AssistantPhaseCommentary, AssistantPhaseFinalAnswer:
		default:
			return &ValidationError{Field: field + ".Phase", Message: fmt.Sprintf("invalid assistant phase %q", item.Phase)}
		}
		if item.Kind != ItemKindMessage || item.Role != RoleAssistant {
			return &ValidationError{Field: field + ".Phase", Message: "phase is only allowed on assistant message items"}
		}
	}

	switch item.Kind {
	case ItemKindMessage:
		if item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: field, Message: "message item must not contain non-message variant fields"}
		}
		if item.Role == "" {
			return &ValidationError{Field: field + ".Role", Message: "role is required for message item"}
		}
		switch item.Role {
		case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
		default:
			return &ValidationError{Field: field + ".Role", Message: fmt.Sprintf("invalid role %q", item.Role)}
		}
		if len(item.Content) == 0 {
			return &ValidationError{Field: field + ".Content", Message: "at least one content part is required for message item"}
		}
		if len(item.Content) > MaxContentPartsPerItem {
			return &ValidationError{Field: field + ".Content", Message: fmt.Sprintf("at most %d content parts per item", MaxContentPartsPerItem)}
		}
		for j, cp := range item.Content {
			if err := cp.validate(fmt.Sprintf("%s.Content[%d]", field, j)); err != nil {
				return err
			}
		}
	case ItemKindItemReference:
		if len(item.Content) > 0 || item.Role != "" || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: field, Message: "item_reference must not contain non-reference variant fields"}
		}
		if item.Reference == nil || strings.TrimSpace(item.Reference.ID) == "" {
			return &ValidationError{Field: field + ".Reference.ID", Message: "reference ID is required for item_reference"}
		}
		if err := validateStringField(field+".Reference.ID", item.Reference.ID, MaxItemReferenceIDBytes); err != nil {
			return err
		}
	case ItemKindToolCall:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: field, Message: "tool_call item must not contain non-tool_call variant fields"}
		}
		if item.ToolCall == nil {
			return &ValidationError{Field: field + ".ToolCall", Message: "tool call data is required for tool_call item"}
		}
		if strings.TrimSpace(item.ToolCall.CallID) == "" {
			return &ValidationError{Field: field + ".ToolCall.CallID", Message: "call ID is required"}
		}
		if strings.TrimSpace(item.ToolCall.Name) == "" {
			return &ValidationError{Field: field + ".ToolCall.Name", Message: "tool name is required"}
		}
		if err := validateStringField(field+".ToolCall.CallID", item.ToolCall.CallID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateExactStringField(field+".ToolCall.Name", item.ToolCall.Name, MaxToolNameBytes); err != nil {
			return err
		}
		if len(item.ToolCall.Arguments) > MaxToolParametersBytes {
			return &ValidationError{Field: field + ".ToolCall.Arguments", Message: fmt.Sprintf("exceeds %d bytes", MaxToolParametersBytes)}
		}
		if len(item.ToolCall.Arguments) > 0 {
			if !json.Valid(item.ToolCall.Arguments) {
				return &ValidationError{Field: field + ".ToolCall.Arguments", Message: "arguments must be valid JSON"}
			}
			if err := validateJSONDepth(item.ToolCall.Arguments, MaxJSONDepth); err != nil {
				return &ValidationError{Field: field + ".ToolCall.Arguments", Message: err.Error()}
			}
		}
	case ItemKindToolResult:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.Reasoning != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: field, Message: "tool_result item must not contain non-tool_result variant fields"}
		}
		if item.ToolResult == nil {
			return &ValidationError{Field: field + ".ToolResult", Message: "tool result data is required for tool_result item"}
		}
		if strings.TrimSpace(item.ToolResult.CallID) == "" {
			return &ValidationError{Field: field + ".ToolResult.CallID", Message: "call ID is required"}
		}
		if err := validateStringField(field+".ToolResult.CallID", item.ToolResult.CallID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateExactStringField(field+".ToolResult.Name", item.ToolResult.Name, MaxToolNameBytes); err != nil {
			return err
		}
		hasOutput := item.ToolResult.Output != ""
		hasParts := len(item.ToolResult.Parts) > 0
		if hasOutput && hasParts {
			return &ValidationError{Field: field + ".ToolResult", Message: "tool result must specify output or parts, not both"}
		}
		if !hasOutput && !hasParts {
			return &ValidationError{Field: field + ".ToolResult", Message: "tool result must specify output or parts"}
		}
		if len(item.ToolResult.Output) > MaxPartTextBytes {
			return &ValidationError{Field: field + ".ToolResult.Output", Message: fmt.Sprintf("output exceeds %d bytes", MaxPartTextBytes)}
		}
		if len(item.ToolResult.Parts) > MaxContentPartsPerItem {
			return &ValidationError{Field: field + ".ToolResult.Parts", Message: fmt.Sprintf("at most %d parts per tool result", MaxContentPartsPerItem)}
		}
		for j, cp := range item.ToolResult.Parts {
			if err := cp.validate(fmt.Sprintf("%s.ToolResult.Parts[%d]", field, j)); err != nil {
				return err
			}
		}
	case ItemKindReasoning:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Compaction != nil || item.Extension != nil {
			return &ValidationError{Field: field, Message: "reasoning item must not contain non-reasoning variant fields"}
		}
		if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
			return &ValidationError{Field: field + ".Reasoning", Message: "reasoning payload is required for reasoning item"}
		}
		if err := validateReasoningPart(item.Reasoning.Reasoning); err != nil {
			return &ValidationError{Field: field + ".Reasoning", Message: err.Error()}
		}
	case ItemKindCompaction:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Extension != nil {
			return &ValidationError{Field: field, Message: "compaction item must not contain non-compaction variant fields"}
		}
		if item.Compaction == nil {
			return &ValidationError{Field: field + ".Compaction", Message: "compaction data is required for compaction item"}
		}
		if err := validateStringField(field+".Compaction.EncapsulatedID", item.Compaction.EncapsulatedID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".Compaction.Dialect", item.Compaction.Dialect, MaxCompactionDialectBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".Compaction.Implementor", item.Compaction.Implementor, MaxExtensionImplementorBytes); err != nil {
			return err
		}
		if err := validateStringField(field+".Compaction.EncryptedContent", item.Compaction.EncryptedContent, MaxCompactionEncryptedContentBytes); err != nil {
			return err
		}
		if len(item.Compaction.Opaque) > MaxPartJSONBytes {
			return &ValidationError{Field: field + ".Compaction.Opaque", Message: fmt.Sprintf("opaque compaction data exceeds %d bytes", MaxPartJSONBytes)}
		}
		if len(item.Compaction.Opaque) > 0 {
			if !json.Valid(item.Compaction.Opaque) {
				return &ValidationError{Field: field + ".Compaction.Opaque", Message: "opaque compaction data must be valid JSON"}
			}
			if err := validateJSONDepth(item.Compaction.Opaque, MaxJSONDepth); err != nil {
				return &ValidationError{Field: field + ".Compaction.Opaque", Message: err.Error()}
			}
		}
	case ItemKindExtension:
		if len(item.Content) > 0 || item.Role != "" || item.Reference != nil || item.ToolCall != nil || item.ToolResult != nil || item.Reasoning != nil || item.Compaction != nil {
			return &ValidationError{Field: field, Message: "extension item must not contain non-extension variant fields"}
		}
		if item.Extension == nil {
			return &ValidationError{Field: field + ".Extension", Message: "extension record is required for extension item"}
		}
		if err := item.Extension.validate(field + ".Extension"); err != nil {
			return err
		}
	case "":
		return &ValidationError{Field: field + ".Kind", Message: "item kind is required"}
	default:
		return &ValidationError{Field: field + ".Kind", Message: fmt.Sprintf("unknown item kind %q", item.Kind)}
	}
	return nil
}
