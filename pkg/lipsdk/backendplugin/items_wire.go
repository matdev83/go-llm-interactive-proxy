package backendplugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// ApplyOrderedItemWire projects item authority, operation metadata, and requirements into plugin DTO fields.
func ApplyOrderedItemWire(inv *Invocation, call lipapi.Call) {
	if inv == nil {
		return
	}
	if op := string(call.Invocation.Operation); op != "" {
		inv.Operation = op
	}
	if dm := string(call.Invocation.DeliveryMode); dm != "" {
		inv.DeliveryMode = dm
	}
	if tm := string(call.Invocation.TransportMode); tm != "" {
		inv.TransportMode = tm
	}
	if pck := strings.TrimSpace(call.PromptCacheKey); pck != "" {
		inv.PromptCacheKey = pck
	}
	if !call.HasItemAuthority() {
		return
	}
	inv.ItemAuthority = true
	inv.Items = mapItemsToDTO(call.Items)
	inv.ProtocolRequirements = mapRequirementsToDTO(lipapi.DeriveProtocolRequirements(call))
}

// HasItemAuthorityWire reports whether the invocation carries ordered item authority.
func HasItemAuthorityWire(inv Invocation) bool {
	return inv.ItemAuthority && len(inv.Items) > 0
}

func mapRequirementsToDTO(req lipapi.ProtocolRequirements) ProtocolRequirementsDTO {
	out := ProtocolRequirementsDTO{
		Capabilities: make([]string, 0, len(req.Capabilities)),
	}
	for _, c := range req.Capabilities {
		out.Capabilities = append(out.Capabilities, string(c))
	}
	for _, d := range req.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, DialectRequirementDTO{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, d := range req.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, DialectRequirementDTO{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, d := range req.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, DialectRequirementDTO{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, e := range req.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, ExtensionRequirementDTO{
			Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor,
		})
	}
	return out
}

func mapItemsToDTO(items []lipapi.Item) []InvocationItem {
	out := make([]InvocationItem, 0, len(items))
	for _, item := range items {
		dto := InvocationItem{
			Kind:   string(item.Kind),
			ID:     item.ID,
			Status: string(item.Status),
			Role:   item.Role,
			Phase:  string(item.Phase),
		}
		for _, cp := range item.Content {
			dto.Content = append(dto.Content, mapContentPartToDTO(cp))
		}
		if item.ToolCall != nil {
			dto.ToolCall = &InvocationToolCall{
				CallID:    item.ToolCall.CallID,
				Name:      item.ToolCall.Name,
				Arguments: RawJSONFromBytes(item.ToolCall.Arguments),
			}
		}
		if item.ToolResult != nil {
			tr := &InvocationToolResult{
				CallID: item.ToolResult.CallID,
				Name:   item.ToolResult.Name,
			}
			if item.ToolResult.Output != "" {
				o := item.ToolResult.Output
				tr.Output = &o
			}
			for _, cp := range item.ToolResult.Parts {
				tr.StructuredParts = append(tr.StructuredParts, mapContentPartToDTO(cp))
			}
			dto.ToolResult = tr
		}
		if item.Reference != nil {
			dto.ItemReference = &InvocationItemReference{ID: item.Reference.ID}
		}
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
			d := string(item.Reasoning.Reasoning.Dialect)
			t := item.Reasoning.Reasoning.Text
			sig := item.Reasoning.Reasoning.Signature
			rp := item.Reasoning.Reasoning
			dto.Reasoning = &InvocationReasoningItem{
				Dialect:   &d,
				Text:      &t,
				Signature: strPtrIfNonEmpty(sig),
				Opaque:    RawJSONFromBytes(rp.Opaque),
			}
			mapReasoningExactFields(rp, &dto.Reasoning.Summary, &dto.Reasoning.Content, &dto.Reasoning.EncryptedContent)
		}
		if item.Compaction != nil {
			dto.Compaction = &InvocationCompactionItem{
				EncapsulatedID:   item.Compaction.EncapsulatedID,
				Dialect:          item.Compaction.Dialect,
				Implementor:      item.Compaction.Implementor,
				Opaque:           RawJSONFromBytes(item.Compaction.Opaque),
				EncryptedContent: item.Compaction.EncryptedContent,
			}
		}
		if item.Extension != nil {
			dto.Extension = &InvocationExtensionItem{
				Namespace:   item.Extension.Namespace,
				Type:        item.Extension.Type,
				Implementor: item.Extension.Implementor,
				Direction:   item.Extension.Direction,
				Opaque:      RawJSONFromBytes(item.Extension.Data),
			}
		}
		out = append(out, dto)
	}
	return out
}

func mapContentPartToDTO(cp lipapi.ContentPart) InvocationContentPart {
	part := InvocationContentPart{Kind: mapContentPartKind(cp.Kind)}
	switch cp.Kind {
	case lipapi.ContentPartText:
		t := cp.Text
		part.Text = &t
	case lipapi.ContentPartImageRef:
		r := cp.ImageRef
		part.ImageRef = &r
		if cp.ImageMIME != "" {
			m := cp.ImageMIME
			part.ImageMIME = &m
		}
	case lipapi.ContentPartFileRef:
		r := cp.FileRef
		part.FileRef = &r
		if cp.FileMIME != "" {
			m := cp.FileMIME
			part.FileMIME = &m
		}
		if cp.FileName != "" {
			n := cp.FileName
			part.FileName = &n
		}
		if cp.FileData != "" {
			fd := cp.FileData
			part.FileData = &fd
		}
	case lipapi.ContentPartVideoRef:
		r := cp.VideoRef
		part.VideoRef = &r
		if cp.VideoMIME != "" {
			m := cp.VideoMIME
			part.VideoMIME = &m
		}
	case lipapi.ContentPartRefusal:
		r := cp.Refusal
		part.Refusal = &r
	case lipapi.ContentPartSummary:
		s := cp.Summary
		part.Summary = &s
	case lipapi.ContentPartAssistantRef:
		r := cp.AssistantRef
		part.AssistantRef = &r
	case lipapi.ContentPartReasoning:
		if cp.Reasoning != nil {
			d := string(cp.Reasoning.Dialect)
			t := cp.Reasoning.Text
			part.Reasoning = &InvocationReasoningPart{Dialect: &d, Text: &t}
			if cp.Reasoning.Signature != "" {
				sig := cp.Reasoning.Signature
				part.Reasoning.Signature = &sig
			}
			if len(cp.Reasoning.Opaque) > 0 {
				part.Reasoning.Opaque = RawJSONFromBytes(cp.Reasoning.Opaque)
			}
			mapReasoningExactFields(cp.Reasoning,
				&part.Reasoning.Summary, &part.Reasoning.Content, &part.Reasoning.EncryptedContent)
		}
	case lipapi.ContentPartAnnotation:
		if cp.Annotation != nil {
			typ := cp.Annotation.Type
			part.AnnotationType = &typ
			if len(cp.Annotation.Data) > 0 {
				part.AnnotationData = RawJSONFromBytes(cp.Annotation.Data)
			}
		}
	case lipapi.ContentPartJSON:
		t := cp.Text
		part.Text = &t
	case lipapi.ContentPartToolResult:
		t := cp.Text
		part.Text = &t
	case lipapi.ContentPartExtension:
		if cp.Extension != nil {
			typ := cp.Extension.Type
			part.ExtensionType = &typ
			if len(cp.Extension.Data) > 0 {
				part.ExtensionData = RawJSONFromBytes(cp.Extension.Data)
			}
		}
	}
	return part
}

func strPtrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// mapReasoningExactFields projects canonical OpenAI Responses reasoning-item
// exact fields (summary/content arrays, nullable encrypted_content) into ABI
// RawJSON carriers with absent/null/value presence.
func mapReasoningExactFields(rp *lipapi.ReasoningPart, summary, content, encrypted *RawJSON) {
	*summary = RawJSONAbsentValue()
	*content = RawJSONAbsentValue()
	*encrypted = RawJSONAbsentValue()
	if rp == nil {
		return
	}
	if rp.SummaryPresent || len(rp.Summary) > 0 {
		*summary = RawJSONFromBytes(rp.Summary)
	}
	if rp.ContentPresent || len(rp.Content) > 0 {
		*content = RawJSONFromBytes(rp.Content)
	}
	if rp.EncryptedContentPresent {
		if isJSONNullBytes(rp.EncryptedContent) {
			*encrypted = RawJSONNullValue()
		} else {
			*encrypted = RawJSONFromBytes(rp.EncryptedContent)
		}
	}
}

// applyReasoningExactFields restores canonical OpenAI Responses reasoning-item
// exact fields from ABI RawJSON carriers with absent/null/value presence.
func applyReasoningExactFields(summary, content, encrypted RawJSON, out *lipapi.ReasoningPart) {
	if out == nil {
		return
	}
	switch summary.State() {
	case RawJSONValue:
		out.Summary = append(json.RawMessage(nil), summary.Bytes()...)
		out.SummaryPresent = true
	}
	switch content.State() {
	case RawJSONValue:
		out.Content = append(json.RawMessage(nil), content.Bytes()...)
		out.ContentPresent = true
	}
	switch encrypted.State() {
	case RawJSONNull:
		out.EncryptedContent = json.RawMessage("null")
		out.EncryptedContentPresent = true
	case RawJSONValue:
		out.EncryptedContent = append(json.RawMessage(nil), encrypted.Bytes()...)
		out.EncryptedContentPresent = true
	}
}

func isJSONNullBytes(b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}

func mapContentPartKind(k lipapi.ContentPartKind) PartKind {
	switch k {
	case lipapi.ContentPartText:
		return PartKindText
	case lipapi.ContentPartJSON:
		return PartKindJSON
	case lipapi.ContentPartToolResult:
		return PartKindToolResult
	case lipapi.ContentPartImageRef:
		return PartKindImageRef
	case lipapi.ContentPartFileRef:
		return PartKindFileRef
	case lipapi.ContentPartVideoRef:
		return PartKindVideoRef
	case lipapi.ContentPartReasoning:
		return PartKindReasoning
	case lipapi.ContentPartRefusal:
		return PartKindRefusal
	case lipapi.ContentPartSummary:
		return PartKindSummary
	case lipapi.ContentPartAnnotation:
		return PartKindAnnotation
	case lipapi.ContentPartAssistantRef:
		return PartKindAssistantRef
	case lipapi.ContentPartExtension:
		return PartKindExtension
	default:
		return PartKindUnspecified
	}
}

// checkOrderedItemContentABIRepresentable verifies that item-authority content
// parts can be carried on the backendplugin ABI without silent semantic loss.
// Exact OpenAI Responses semantics (inline file_data, opaque extension content
// parts, reasoning exact fields, compaction encrypted_content) are gated by
// RequireExactOpenResponsesABISupport before execution; this function remains
// as a structural safety net for anything the ABI genuinely cannot represent.
func checkOrderedItemContentABIRepresentable(items []lipapi.Item) error {
	for i, item := range items {
		for j, cp := range item.Content {
			if err := checkABIContentPart(cp, fmt.Sprintf("Items[%d].Content[%d]", i, j)); err != nil {
				return err
			}
		}
		if item.ToolResult != nil {
			for j, cp := range item.ToolResult.Parts {
				if err := checkABIContentPart(cp, fmt.Sprintf("Items[%d].ToolResult.Parts[%d]", i, j)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkABIContentPart rejects canonical content parts the backendplugin ABI
// cannot represent under any negotiated minor. Exact OpenAI Responses semantics
// (inline file_data, opaque extension parts, reasoning exact fields) are carried
// at minor >= 3 and gated by RequireExactOpenResponsesABISupport before execution.
func checkABIContentPart(cp lipapi.ContentPart, field string) error {
	if mapContentPartKind(cp.Kind) == PartKindUnspecified {
		return fmt.Errorf("%w: %s: unknown content part kind %q", ErrUnsupportedPartKind, field, cp.Kind)
	}
	return nil
}
