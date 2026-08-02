package backendplugin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func applyInvocationWireToCall(call *lipapi.Call, inv Invocation) error {
	if inv.Operation != "" {
		call.Invocation.Operation = lipapi.Operation(inv.Operation)
	}
	if inv.DeliveryMode != "" {
		call.Invocation.DeliveryMode = lipapi.DeliveryMode(inv.DeliveryMode)
	}
	if inv.TransportMode != "" {
		call.Invocation.TransportMode = lipapi.TransportMode(inv.TransportMode)
	}
	if !inv.ItemAuthority {
		return nil
	}
	items, err := itemsFromInvocationDTO(inv.Items)
	if err != nil {
		return err
	}
	call.Items = items
	return nil
}

func itemsFromInvocationDTO(in []InvocationItem) ([]lipapi.Item, error) {
	out := make([]lipapi.Item, 0, len(in))
	for i, item := range in {
		mapped, err := itemFromInvocationDTO(item, fmt.Sprintf("Items[%d]", i))
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func itemFromInvocationDTO(item InvocationItem, field string) (lipapi.Item, error) {
	out := lipapi.Item{
		Kind:   lipapi.ItemKind(item.Kind),
		ID:     item.ID,
		Status: lipapi.ItemStatus(item.Status),
		Phase:  lipapi.AssistantPhase(item.Phase),
	}
	if item.Role != "" && strings.ToLower(strings.TrimSpace(item.Kind)) != "tool_result" {
		out.Role = item.Role
	}
	for j, cp := range item.Content {
		part, err := contentPartFromInvocationDTO(cp, fmt.Sprintf("%s.Content[%d]", field, j))
		if err != nil {
			return lipapi.Item{}, err
		}
		out.Content = append(out.Content, part)
	}
	if item.ToolCall != nil {
		args := item.ToolCall.Arguments.Bytes()
		out.ToolCall = &lipapi.ToolCallItem{
			CallID:    item.ToolCall.CallID,
			Name:      item.ToolCall.Name,
			Arguments: append(json.RawMessage(nil), args...),
		}
	}
	if item.ToolResult != nil {
		tr := &lipapi.ToolResultItem{
			CallID: item.ToolResult.CallID,
			Name:   item.ToolResult.Name,
		}
		if item.ToolResult.Output != nil {
			tr.Output = *item.ToolResult.Output
		}
		for j, cp := range item.ToolResult.StructuredParts {
			part, err := contentPartFromInvocationDTO(cp, fmt.Sprintf("%s.ToolResult.StructuredParts[%d]", field, j))
			if err != nil {
				return lipapi.Item{}, err
			}
			tr.Parts = append(tr.Parts, part)
		}
		out.ToolResult = tr
	}
	if item.ItemReference != nil {
		out.Reference = &lipapi.ItemReference{ID: item.ItemReference.ID}
	}
	if item.Reasoning != nil {
		rp, err := reasoningPartFromInvocationItem(*item.Reasoning, field+".Reasoning")
		if err != nil {
			return lipapi.Item{}, err
		}
		out.Reasoning = &lipapi.ReasoningItem{Reasoning: rp}
	}
	if item.Compaction != nil {
		out.Compaction = &lipapi.CompactionItem{
			EncapsulatedID: item.Compaction.EncapsulatedID,
			Dialect:        item.Compaction.Dialect,
			Implementor:    item.Compaction.Implementor,
			Opaque:         append(json.RawMessage(nil), item.Compaction.Opaque.Bytes()...),
		}
	}
	if item.Extension != nil {
		out.Extension = &lipapi.OpaqueExtension{
			Namespace:   item.Extension.Namespace,
			Type:        item.Extension.Type,
			Implementor: item.Extension.Implementor,
			Direction:   item.Extension.Direction,
			Data:        append(json.RawMessage(nil), item.Extension.Opaque.Bytes()...),
		}
	}
	return out, nil
}

func contentPartFromInvocationDTO(cp InvocationContentPart, field string) (lipapi.ContentPart, error) {
	out := lipapi.ContentPart{Kind: mapContentPartKindToLipapi(cp.Kind)}
	switch cp.Kind {
	case PartKindText:
		if cp.Text == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires text", ErrInvalidInvocation, field)
		}
		out.Text = *cp.Text
	case PartKindJSON:
		if cp.Text == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires json text", ErrInvalidInvocation, field)
		}
		out.Text = *cp.Text
	case PartKindToolResult:
		if cp.Text == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires tool result text", ErrInvalidInvocation, field)
		}
		out.Text = *cp.Text
	case PartKindImageRef:
		if cp.ImageRef == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires image_ref", ErrInvalidInvocation, field)
		}
		out.ImageRef = *cp.ImageRef
		if cp.ImageMIME != nil {
			out.ImageMIME = *cp.ImageMIME
		}
	case PartKindFileRef:
		if cp.FileRef == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires file_ref", ErrInvalidInvocation, field)
		}
		out.FileRef = *cp.FileRef
		if cp.FileMIME != nil {
			out.FileMIME = *cp.FileMIME
		}
		if cp.FileName != nil {
			out.FileName = *cp.FileName
		}
	case PartKindVideoRef:
		if cp.VideoRef == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires video_ref", ErrInvalidInvocation, field)
		}
		out.VideoRef = *cp.VideoRef
		if cp.VideoMIME != nil {
			out.VideoMIME = *cp.VideoMIME
		}
	case PartKindRefusal:
		if cp.Refusal == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires refusal", ErrInvalidInvocation, field)
		}
		out.Refusal = *cp.Refusal
	case PartKindSummary:
		if cp.Summary == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires summary", ErrInvalidInvocation, field)
		}
		out.Summary = *cp.Summary
	case PartKindAssistantRef:
		if cp.AssistantRef == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires assistant_ref", ErrInvalidInvocation, field)
		}
		out.AssistantRef = *cp.AssistantRef
	case PartKindReasoning:
		if cp.Reasoning == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires reasoning", ErrInvalidInvocation, field)
		}
		rp, err := reasoningPartFromInvocationPart(*cp.Reasoning, field+".Reasoning")
		if err != nil {
			return lipapi.ContentPart{}, err
		}
		out.Reasoning = rp
	case PartKindAnnotation:
		if cp.AnnotationType == nil {
			return lipapi.ContentPart{}, fmt.Errorf("%w: %s requires annotation type", ErrInvalidInvocation, field)
		}
		ann := &lipapi.AnnotationPart{Type: *cp.AnnotationType}
		if cp.AnnotationData.State() == RawJSONValue {
			ann.Data = append(json.RawMessage(nil), cp.AnnotationData.Bytes()...)
		}
		out.Annotation = ann
	default:
		return lipapi.ContentPart{}, fmt.Errorf("%w: %s unsupported kind %q", ErrInvalidInvocation, field, cp.Kind)
	}
	return out, nil
}

func reasoningPartFromInvocationItem(r InvocationReasoningItem, field string) (*lipapi.ReasoningPart, error) {
	out := &lipapi.ReasoningPart{}
	if r.Dialect != nil {
		out.Dialect = lipapi.ReasoningDialect(*r.Dialect)
	}
	if r.Text != nil {
		out.Text = *r.Text
	}
	if r.Signature != nil {
		out.Signature = *r.Signature
	}
	if r.Opaque.State() == RawJSONValue {
		out.Opaque = append([]byte(nil), r.Opaque.Bytes()...)
	}
	return out, nil
}

func reasoningPartFromInvocationPart(r InvocationReasoningPart, field string) (*lipapi.ReasoningPart, error) {
	out := &lipapi.ReasoningPart{}
	if r.Dialect != nil {
		out.Dialect = lipapi.ReasoningDialect(*r.Dialect)
	}
	if r.Text != nil {
		out.Text = *r.Text
	}
	if r.Signature != nil {
		out.Signature = *r.Signature
	}
	if r.Opaque.State() == RawJSONValue {
		out.Opaque = append([]byte(nil), r.Opaque.Bytes()...)
	}
	return out, nil
}

func mapContentPartKindToLipapi(k PartKind) lipapi.ContentPartKind {
	switch k {
	case PartKindText:
		return lipapi.ContentPartText
	case PartKindJSON:
		return lipapi.ContentPartJSON
	case PartKindToolResult:
		return lipapi.ContentPartToolResult
	case PartKindImageRef:
		return lipapi.ContentPartImageRef
	case PartKindFileRef:
		return lipapi.ContentPartFileRef
	case PartKindVideoRef:
		return lipapi.ContentPartVideoRef
	case PartKindReasoning:
		return lipapi.ContentPartReasoning
	case PartKindRefusal:
		return lipapi.ContentPartRefusal
	case PartKindSummary:
		return lipapi.ContentPartSummary
	case PartKindAnnotation:
		return lipapi.ContentPartAnnotation
	case PartKindAssistantRef:
		return lipapi.ContentPartAssistantRef
	default:
		return lipapi.ContentPartKind(k)
	}
}
