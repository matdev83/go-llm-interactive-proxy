package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// EncodeRequest converts a canonical lipapi.Call into WireResponseParam and marshals it to JSON bytes.
func EncodeRequest(call lipapi.Call) ([]byte, error) {
	var wireItems []WireItem
	for i, item := range call.Items {
		wItem, err := EncodeItem(item)
		if err != nil {
			return nil, fmt.Errorf("encode item[%d]: %w", i, err)
		}
		wireItems = append(wireItems, wItem)
	}

	inputBytes, err := json.Marshal(ensureNonNilSlice(wireItems))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal input items: %v", ErrEncodeFailed, err)
	}

	var wireTools []WireTool
	for _, t := range call.Tools {
		wireTools = append(wireTools, WireTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  cloneBytes(t.Parameters),
		})
	}

	var toolChoiceBytes json.RawMessage
	if call.ToolChoice.Mode != "" {
		switch call.ToolChoice.Mode {
		case lipapi.ToolChoiceAuto:
			toolChoiceBytes = json.RawMessage(`"auto"`)
		case lipapi.ToolChoiceNone:
			toolChoiceBytes = json.RawMessage(`"none"`)
		case lipapi.ToolChoiceAny:
			toolChoiceBytes = json.RawMessage(`"required"`)
		case lipapi.ToolChoiceRequired:
			if call.ToolChoice.Name != "" {
				tcBytes, err := json.Marshal(WireToolChoiceFunction{
					Type:     "function",
					Function: WireToolChoiceFunctionName{Name: call.ToolChoice.Name},
				})
				if err != nil {
					return nil, fmt.Errorf("%w: failed to marshal tool_choice: %v", ErrEncodeFailed, err)
				}
				toolChoiceBytes = tcBytes
			} else {
				toolChoiceBytes = json.RawMessage(`"required"`)
			}
		}
	}

	param := WireResponseParam{
		Input:             inputBytes,
		Tools:             wireTools,
		ToolChoice:        toolChoiceBytes,
		Temperature:       call.Options.Temperature,
		TopP:              call.Options.TopP,
		MaxOutputTokens:   call.Options.MaxOutputTokens,
		ParallelToolCalls: call.Options.ParallelToolCalls,
	}

	rawMap := make(map[string]json.RawMessage)
	paramBytes, err := json.Marshal(param)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}
	if err := json.Unmarshal(paramBytes, &rawMap); err != nil {
		// Unreachable: paramBytes was produced by json.Marshal(param) above.
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}

	// Merge top-level extensions
	for k, v := range call.Extensions {
		if !json.Valid(v) {
			return nil, fmt.Errorf("%w: invalid extension %q", ErrEncodeFailed, k)
		}
		rawMap[k] = cloneBytes(v)
	}

	return json.Marshal(rawMap)
}

// EncodeItem converts a canonical lipapi.Item into WireItem.
func EncodeItem(item lipapi.Item) (WireItem, error) {
	status := string(item.Status)
	if status == "" {
		status = "completed"
	}

	switch item.Kind {
	case lipapi.ItemKindMessage:
		var wireParts []WireContentPart
		for _, cp := range item.Content {
			wPart := encodeContentPart(cp, item.Role)
			wireParts = append(wireParts, wPart)
		}
		partsBytes, err := json.Marshal(ensureNonNilSlice(wireParts))
		if err != nil {
			return WireItem{}, fmt.Errorf("marshal message content: %w", err)
		}

		return WireItem{
			ID:      item.ID,
			Type:    "message",
			Status:  status,
			Role:    string(item.Role),
			Phase:   string(item.Phase),
			Content: partsBytes,
		}, nil

	case lipapi.ItemKindItemReference:
		refID := ""
		if item.Reference != nil {
			refID = item.Reference.ID
		}
		return WireItem{
			ID:     refID,
			Type:   "item_reference",
			Status: status,
		}, nil

	case lipapi.ItemKindToolCall:
		callID := ""
		name := ""
		var args json.RawMessage
		if item.ToolCall != nil {
			callID = item.ToolCall.CallID
			name = item.ToolCall.Name
			args = wireToolArguments(item.ToolCall.Arguments)
		}
		return WireItem{
			ID:        item.ID,
			Type:      "function_call",
			Status:    status,
			CallID:    callID,
			Name:      name,
			Arguments: args,
		}, nil

	case lipapi.ItemKindToolResult:
		callID := ""
		name := ""
		outputBytes := json.RawMessage(`""`)
		if item.ToolResult != nil {
			callID = item.ToolResult.CallID
			name = item.ToolResult.Name
			if item.ToolResult.Output != "" {
				b, _ := json.Marshal(item.ToolResult.Output)
				outputBytes = b
			} else if len(item.ToolResult.Parts) > 0 {
				var wireParts []WireContentPart
				for _, cp := range item.ToolResult.Parts {
					wireParts = append(wireParts, encodeContentPart(cp, lipapi.RoleTool))
				}
				b, _ := json.Marshal(wireParts)
				outputBytes = b
			}
		}
		return WireItem{
			ID:     item.ID,
			Type:   "function_call_output",
			Status: status,
			CallID: callID,
			Name:   name,
			Output: outputBytes,
		}, nil

	case lipapi.ItemKindReasoning:
		var reasoningBytes json.RawMessage
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
			b, _ := json.Marshal(item.Reasoning.Reasoning.Text)
			reasoningBytes = b
		}
		return WireItem{
			ID:        item.ID,
			Type:      "reasoning",
			Status:    status,
			Reasoning: reasoningBytes,
		}, nil

	case lipapi.ItemKindCompaction:
		encID := ""
		dialect := ""
		implementor := ""
		encrypted := ""
		var opaque json.RawMessage
		if item.Compaction != nil {
			encID = item.Compaction.EncapsulatedID
			dialect = item.Compaction.Dialect
			implementor = item.Compaction.Implementor
			encrypted = item.Compaction.EncryptedContent
			opaque = cloneBytes(item.Compaction.Opaque)
		}
		return WireItem{
			ID:               item.ID,
			Type:             "compaction",
			Status:           status,
			EncapsulatedID:   encID,
			Dialect:          dialect,
			Implementor:      implementor,
			EncryptedContent: encrypted,
			Opaque:           opaque,
		}, nil

	case lipapi.ItemKindExtension:
		wireType := "extension"
		ns := ""
		direction := ""
		var data json.RawMessage
		if item.Extension != nil {
			wireType = item.Extension.Type
			ns = item.Extension.Namespace
			direction = item.Extension.Direction
			data = cloneBytes(item.Extension.Data)
		}
		return WireItem{
			ID:        item.ID,
			Type:      wireType,
			Status:    status,
			Namespace: ns,
			Direction: direction,
			Data:      data,
		}, nil
	}

	return WireItem{}, fmt.Errorf("%w: unknown item kind %q", ErrEncodeFailed, item.Kind)
}

// wireToolArguments normalizes canonical tool call arguments to the pinned
// profile's JSON-string wire form: a JSON string stays verbatim; any other
// canonical argument document (or partial argument text) is wrapped into a
// JSON string. This matches the official 2026-04-24 function_call contract,
// where `arguments` is always a JSON string.
func wireToolArguments(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`""`)
	}
	if trimmed[0] == '"' && json.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	b, _ := json.Marshal(string(trimmed))
	return b
}

// encodeContentPart converts canonical lipapi.ContentPart into WireContentPart.
func encodeContentPart(cp lipapi.ContentPart, role lipapi.Role) WireContentPart {
	switch cp.Kind {
	case lipapi.ContentPartText:
		partType := "input_text"
		if role == lipapi.RoleAssistant {
			partType = "output_text"
		}
		part := WireContentPart{
			Type: partType,
			Text: cp.Text,
		}
		if partType == "output_text" {
			// The pinned profile requires the annotations array on output_text
			// content parts in a response resource.
			part.Annotations = json.RawMessage(`[]`)
		}
		return part

	case lipapi.ContentPartImageRef:
		imgBytes, _ := json.Marshal(cp.ImageRef)
		return WireContentPart{
			Type:     "input_image",
			ImageURL: imgBytes,
		}

	case lipapi.ContentPartRefusal:
		return WireContentPart{
			Type:    "refusal",
			Refusal: cp.Refusal,
		}
	}

	partType := "input_text"
	if role == lipapi.RoleAssistant {
		partType = "output_text"
	}
	return WireContentPart{
		Type: partType,
		Text: cp.Text,
	}
}
