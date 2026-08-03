package backendplugin

import (
	"fmt"

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
			dto.Reasoning = &InvocationReasoningItem{
				Dialect:   &d,
				Text:      &t,
				Signature: strPtrIfNonEmpty(sig),
				Opaque:    RawJSONFromBytes(item.Reasoning.Reasoning.Opaque),
			}
		}
		if item.Compaction != nil {
			dto.Compaction = &InvocationCompactionItem{
				EncapsulatedID: item.Compaction.EncapsulatedID,
				Dialect:        item.Compaction.Dialect,
				Implementor:    item.Compaction.Implementor,
				Opaque:         RawJSONFromBytes(item.Compaction.Opaque),
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
	}
	return part
}

func strPtrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	default:
		return PartKindUnspecified
	}
}

// checkOrderedItemContentABIRepresentable verifies that item-authority content
// parts can be carried on the backendplugin ABI without silent semantic loss.
// Opaque extension content parts and inline file_data are not representable on
// the ABI and are rejected explicitly before execution (never silently dropped).
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
// cannot represent losslessly.
func checkABIContentPart(cp lipapi.ContentPart, field string) error {
	switch cp.Kind {
	case lipapi.ContentPartExtension:
		return fmt.Errorf("%w: %s: opaque extension content parts are not representable on the backendplugin ABI", ErrUnsupportedPartKind, field)
	case lipapi.ContentPartFileRef:
		if cp.FileData != "" {
			return fmt.Errorf("%w: %s: inline file_data is not representable on the backendplugin ABI", ErrUnsupportedPartKind, field)
		}
	}
	return nil
}
