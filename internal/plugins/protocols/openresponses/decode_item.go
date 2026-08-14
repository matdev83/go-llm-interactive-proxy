package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Standard item type discriminators in official OpenResponses 2026-04-24.
var standardItemTypes = map[string]bool{
	"message":              true,
	"item_reference":       true,
	"function_call":        true,
	"function_call_output": true,
	"function_output":      true,
	"reasoning":            true,
	"compaction":           true,
}

// DecodeItem converts a WireItem into a canonical lipapi.Item under the
// caller-provided operational limits.
func DecodeItem(wire WireItem, limits Limits) (lipapi.Item, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if wire.Type == "" {
		return lipapi.Item{}, fmt.Errorf("%w: item missing type discriminator", ErrDecodeFailed)
	}
	if wire.Type == "item_reference" {
		if err := ValidateContinuationRef(wire.ID, limits); err != nil {
			return lipapi.Item{}, err
		}
	}

	// Validate discriminator
	if !standardItemTypes[wire.Type] {
		if !isPrefixedType(wire.Type) {
			return lipapi.Item{}, fmt.Errorf("%w: %q", ErrUnknownDiscriminator, wire.Type)
		}
		// Treat non-standard vendor-prefixed discriminator as Extension item
		status := lipapi.ItemStatus(wire.Status)
		if status == "received" {
			status = lipapi.ItemStatusCompleted
		}
		if err := ValidateOpaquePayloadSize(len(wire.Data), limits); err != nil {
			return lipapi.Item{}, err
		}
		return lipapi.Item{
			Kind:   lipapi.ItemKindExtension,
			ID:     wire.ID,
			Status: status,
			Extension: &lipapi.OpaqueExtension{
				Namespace:   wire.Namespace,
				Type:        wire.Type,
				Implementor: wire.Implementor,
				Direction:   wire.Direction,
				Data:        cloneBytes(wire.Data),
			},
		}, nil
	}

	status := lipapi.ItemStatus(wire.Status)
	if status == "received" {
		status = lipapi.ItemStatusCompleted
	}

	item := lipapi.Item{
		ID:     wire.ID,
		Status: status,
	}

	switch wire.Type {
	case "message":
		item.Kind = lipapi.ItemKindMessage
		item.Role = lipapi.Role(wire.Role)
		item.Phase = lipapi.AssistantPhase(wire.Phase)

		if jsonpresence.IsPresentNonNullJSON(wire.Content) {
			parts, err := decodeContentParts(wire.Content)
			if err != nil {
				return lipapi.Item{}, err
			}
			item.Content = parts
		}

	case "item_reference":
		item.Kind = lipapi.ItemKindItemReference
		item.Reference = &lipapi.ItemReference{
			ID: wire.ID,
		}

	case "function_call":
		item.Kind = lipapi.ItemKindToolCall
		callID := wire.CallID
		if callID == "" {
			callID = wire.ID
		}
		item.ToolCall = &lipapi.ToolCallItem{
			CallID:    callID,
			Name:      wire.Name,
			Arguments: cloneBytes(wire.Arguments),
		}

	case "function_call_output", "function_output":
		item.Kind = lipapi.ItemKindToolResult
		callID := wire.CallID
		if callID == "" {
			callID = wire.ID
		}

		tr := &lipapi.ToolResultItem{
			CallID: callID,
			Name:   wire.Name,
		}

		if len(bytes.TrimSpace(wire.Output)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Output)) {
			return lipapi.Item{}, fmt.Errorf("%w: tool_result output cannot be null", ErrDecodeFailed)
		}
		if jsonpresence.IsPresentNonNullJSON(wire.Output) {
			trimmed := bytes.TrimSpace(wire.Output)
			if len(trimmed) > 0 && trimmed[0] == '"' {
				var outStr string
				if err := json.Unmarshal(trimmed, &outStr); err == nil {
					tr.Output = outStr
				} else {
					// A malformed JSON string is not raw text. Reject it rather than
					// persisting syntax quotes or an injected structural payload.
					return lipapi.Item{}, fmt.Errorf("%w: tool_result output must be a valid string", ErrDecodeFailed)
				}
			} else if len(trimmed) > 0 && trimmed[0] == '[' {
				parts, err := decodeContentParts(wire.Output)
				if err != nil {
					return lipapi.Item{}, fmt.Errorf("tool_result parts: %w", err)
				}
				tr.Parts = parts
			} else {
				if json.Valid(trimmed) {
					return lipapi.Item{}, fmt.Errorf("%w: tool_result output cannot be a non-string JSON primitive", ErrDecodeFailed)
				}
				if len(trimmed) > 0 && trimmed[0] == '{' {
					return lipapi.Item{}, fmt.Errorf("%w: tool_result output object is not a string or content array", ErrDecodeFailed)
				}
				tr.Output = string(trimmed)
			}
		}
		item.ToolResult = tr

	case "reasoning":
		item.Kind = lipapi.ItemKindReasoning
		rItem := &lipapi.ReasoningItem{
			Reasoning: &lipapi.ReasoningPart{
				Dialect: lipapi.ReasoningDialect("openresponses.reasoning.v1"),
			},
		}

		if len(bytes.TrimSpace(wire.Reasoning)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Reasoning)) {
			return lipapi.Item{}, fmt.Errorf("%w: reasoning cannot be null", ErrDecodeFailed)
		}
		if wire.Signature != "" {
			rItem.Reasoning.Signature = wire.Signature
		}
		if jsonpresence.IsPresentNonNullJSON(wire.Opaque) {
			rItem.Reasoning.Opaque = cloneBytes(wire.Opaque)
		}
		if len(wire.Summary) > 0 {
			rItem.Reasoning.Summary = cloneBytes(wire.Summary)
			rItem.Reasoning.SummaryPresent = wire.SummaryPresent || jsonpresence.IsPresentNonNullJSON(wire.Summary)
		}
		if len(wire.Content) > 0 {
			rItem.Reasoning.Content = cloneBytes(wire.Content)
			rItem.Reasoning.ContentPresent = wire.ContentPresent || jsonpresence.IsPresentNonNullJSON(wire.Content)
		}
		if wire.ReasoningEncryptedContentPresent {
			rItem.Reasoning.EncryptedContentPresent = true
			rItem.Reasoning.EncryptedContent = cloneBytes(wire.ReasoningEncryptedContent)
		}
		if jsonpresence.IsPresentNonNullJSON(wire.Reasoning) {
			text, signature, opaque, err := decodeReasoningPayload(wire.Reasoning)
			if err != nil {
				return lipapi.Item{}, err
			}
			rItem.Reasoning.Text = text
			if signature != "" {
				rItem.Reasoning.Signature = signature
			}
			if len(opaque) > 0 {
				rItem.Reasoning.Opaque = opaque
			}
		} else if len(bytes.TrimSpace(wire.Content)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Content)) {
			return lipapi.Item{}, fmt.Errorf("%w: reasoning content cannot be null", ErrDecodeFailed)
		} else if jsonpresence.IsPresentNonNullJSON(wire.Content) {
			trimmed := bytes.TrimSpace(wire.Content)
			if len(trimmed) > 0 && trimmed[0] == '"' {
				var cStr string
				if err := json.Unmarshal(trimmed, &cStr); err != nil {
					return lipapi.Item{}, fmt.Errorf("%w: reasoning content is not a string union", ErrDecodeFailed)
				}
				rItem.Reasoning.Text = cStr
			} else if len(trimmed) > 0 && trimmed[0] == '[' {
				parts, err := decodeContentParts(wire.Content)
				if err != nil {
					return lipapi.Item{}, fmt.Errorf("%w: reasoning content: %v", ErrDecodeFailed, err)
				}
				var sb strings.Builder
				for _, p := range parts {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(p.Text)
				}
				rItem.Reasoning.Text = sb.String()
			} else {
				if json.Valid(trimmed) {
					return lipapi.Item{}, fmt.Errorf("%w: reasoning content cannot be a non-string JSON primitive", ErrDecodeFailed)
				}
				return lipapi.Item{}, fmt.Errorf("%w: reasoning content must be a string or content array", ErrDecodeFailed)
			}
		}
		item.Reasoning = rItem

	case "compaction":
		if err := ValidateOpaquePayloadSize(len(wire.Opaque), limits); err != nil {
			return lipapi.Item{}, err
		}
		if len(wire.EncryptedContent) > lipapi.MaxCompactionEncryptedContentBytes {
			return lipapi.Item{}, fmt.Errorf("%w: compaction encrypted_content exceeds %d bytes", ErrDecodeFailed, lipapi.MaxCompactionEncryptedContentBytes)
		}
		item.Kind = lipapi.ItemKindCompaction
		item.Compaction = &lipapi.CompactionItem{
			EncapsulatedID:   wire.EncapsulatedID,
			Dialect:          wire.Dialect,
			Implementor:      wire.Implementor,
			EncryptedContent: wire.EncryptedContent,
			Opaque:           cloneBytes(wire.Opaque),
		}
	}

	return item, nil
}

func decodeReasoningPayload(raw []byte) (text, signature string, opaque json.RawMessage, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", "", nil, fmt.Errorf("%w: reasoning must be a valid string", ErrDecodeFailed)
		}
		return text, "", nil, nil
	}
	var obj struct {
		Text      string            `json:"text"`
		Signature string            `json:"signature"`
		Opaque    json.RawMessage   `json:"opaque"`
		Summary   []WireContentPart `json:"summary"`
		Content   []WireContentPart `json:"content"`
		Encrypted string            `json:"encrypted_content"`
	}
	if !json.Valid(trimmed) {
		// Older OpenResponses adapters carried plain reasoning text in the raw
		// field. Preserve that established compatibility form.
		return string(trimmed), "", nil, nil
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return "", "", nil, fmt.Errorf("%w: reasoning must be a valid string or object", ErrDecodeFailed)
	}
	text, signature, opaque = obj.Text, obj.Signature, cloneBytes(obj.Opaque)
	for _, part := range append(obj.Summary, obj.Content...) {
		if part.Text == "" {
			continue
		}
		if text != "" {
			text += "\n"
		}
		text += part.Text
	}
	if len(opaque) == 0 && obj.Encrypted != "" {
		opaque, _ = json.Marshal(obj.Encrypted)
	}
	return text, signature, opaque, nil
}
