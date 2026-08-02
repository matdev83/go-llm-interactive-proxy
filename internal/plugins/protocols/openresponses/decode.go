package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/jsonpresence"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	MaxRequestBytes = 8 * 1024 * 1024 // 8 MiB max request body
	MaxJSONDepth    = 64
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

// Standard content part type discriminators in official OpenResponses 2026-04-24.
var standardContentPartTypes = map[string]bool{
	"input_text":  true,
	"output_text": true,
	"text":        true,
	"input_image": true,
	"refusal":     true,
}

// isPrefixedType returns true if the discriminator is a vendor-prefixed extension (containing ':' or '/').
func isPrefixedType(t string) bool {
	return strings.Contains(t, ":") || strings.Contains(t, "/")
}

// validateJSONStrict checks for valid UTF-8, duplicate object keys, maximum JSON depth, and trailing data.
func validateJSONStrict(data []byte, maxDepth int) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("%w: invalid UTF-8 encoding", ErrDecodeFailed)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	type stateKind int
	const (
		stateObject stateKind = iota
		stateArray
	)

	type frame struct {
		kind         stateKind
		seenKeys     map[string]bool
		expectingKey bool
	}

	var stack []frame
	depth := 0
	hasReadRoot := false

	for {
		t, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrDecodeFailed, err)
		}

		if hasReadRoot && len(stack) == 0 {
			return ErrTrailingData
		}

		switch v := t.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth++
				if depth > maxDepth {
					return fmt.Errorf("%w: JSON depth %d exceeds limit %d", ErrDecodeFailed, depth, maxDepth)
				}
				if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
					top := &stack[len(stack)-1]
					if top.expectingKey {
						return fmt.Errorf("%w: expected object key, got '{'", ErrDecodeFailed)
					}
				}
				stack = append(stack, frame{
					kind:         stateObject,
					seenKeys:     make(map[string]bool),
					expectingKey: true,
				})
			case '}':
				if len(stack) == 0 || stack[len(stack)-1].kind != stateObject {
					return fmt.Errorf("%w: unexpected '}'", ErrDecodeFailed)
				}
				stack = stack[:len(stack)-1]
				depth--
				if len(stack) == 0 {
					hasReadRoot = true
				} else if stack[len(stack)-1].kind == stateObject {
					stack[len(stack)-1].expectingKey = true
				}
			case '[':
				depth++
				if depth > maxDepth {
					return fmt.Errorf("%w: JSON depth %d exceeds limit %d", ErrDecodeFailed, depth, maxDepth)
				}
				if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
					top := &stack[len(stack)-1]
					if top.expectingKey {
						return fmt.Errorf("%w: expected object key, got '['", ErrDecodeFailed)
					}
				}
				stack = append(stack, frame{
					kind: stateArray,
				})
			case ']':
				if len(stack) == 0 || stack[len(stack)-1].kind != stateArray {
					return fmt.Errorf("%w: unexpected ']'", ErrDecodeFailed)
				}
				stack = stack[:len(stack)-1]
				depth--
				if len(stack) == 0 {
					hasReadRoot = true
				} else if stack[len(stack)-1].kind == stateObject {
					stack[len(stack)-1].expectingKey = true
				}
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
				top := &stack[len(stack)-1]
				if top.expectingKey {
					if top.seenKeys[v] {
						return fmt.Errorf("%w: duplicate key %q", ErrDecodeFailed, v)
					}
					top.seenKeys[v] = true
					top.expectingKey = false
				} else {
					top.expectingKey = true
				}
			} else if len(stack) == 0 {
				hasReadRoot = true
			}
		default: // bool, number, null
			if len(stack) > 0 && stack[len(stack)-1].kind == stateObject {
				top := &stack[len(stack)-1]
				if top.expectingKey {
					return fmt.Errorf("%w: expected object key, got value", ErrDecodeFailed)
				}
				top.expectingKey = true
			} else if len(stack) == 0 {
				hasReadRoot = true
			}
		}
	}

	if len(stack) > 0 {
		return fmt.Errorf("%w: unclosed JSON structure", ErrDecodeFailed)
	}

	if dec.More() {
		return ErrTrailingData
	}

	return nil
}

// DecodeRequest unmarshals JSON request bytes into WireResponseParam and converts it to a canonical lipapi.Call.
// An omitted limits argument uses DefaultLimits for compatibility with existing
// internal callers; production frontends may pass their validated limits.
func DecodeRequest(data []byte, configured ...Limits) (*WireResponseParam, lipapi.Call, error) {
	limits := DefaultLimits()
	if len(configured) > 0 && configured[0] != (Limits{}) {
		limits = configured[0]
	}
	if limits.MaxItemDepth <= 0 {
		limits.MaxItemDepth = MaxJSONDepth
	}
	if len(data) == 0 {
		return nil, lipapi.Call{}, fmt.Errorf("%w: empty request payload", ErrDecodeFailed)
	}
	if err := ValidateRequestBytes(data, limits); err != nil {
		return nil, lipapi.Call{}, err
	}

	// Perform strict JSON validation for UTF-8, duplicate keys, depth, and trailing data.
	if err := validateJSONStrict(data, limits.MaxItemDepth); err != nil {
		return nil, lipapi.Call{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var rawMap map[string]json.RawMessage
	if err := decoder.Decode(&rawMap); err != nil {
		return nil, lipapi.Call{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}

	// Unmarshal into WireResponseParam
	var param WireResponseParam
	if err := json.Unmarshal(data, &param); err != nil {
		return nil, lipapi.Call{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}

	// Check for conflicting legacy fields (e.g. "messages" or "instructions" alongside "input")
	if _, hasMessages := rawMap["messages"]; hasMessages {
		if _, hasInput := rawMap["input"]; hasInput {
			return nil, lipapi.Call{}, ErrDuplicateAuthority
		}
	}

	// Reject background=true
	if param.Background != nil && *param.Background {
		return nil, lipapi.Call{}, ErrUnsupportedBackground
	}

	// Collect extra top-level fields
	param.ExtraFields = make(map[string]json.RawMessage)
	knownFields := map[string]bool{
		"model": true, "input": true, "instructions": true, "tools": true,
		"tool_choice": true, "parallel_tool_calls": true, "temperature": true,
		"top_p": true, "max_output_tokens": true, "max_tool_calls": true,
		"truncation": true, "text": true, "reasoning": true, "store": true,
		"background": true, "previous_response_id": true, "metadata": true,
		"service_tier": true, "safety_identifier": true, "prompt_cache_key": true,
		"prompt_cache_retention": true, "messages": true,
	}

	extensions := make(map[string]json.RawMessage)
	for k, v := range rawMap {
		if !knownFields[k] {
			param.ExtraFields[k] = cloneBytes(v)
			if isPrefixedType(k) {
				extensions[k] = cloneBytes(v)
			} else {
				return nil, lipapi.Call{}, fmt.Errorf("%w: unknown top-level field %q", ErrUnknownDiscriminator, k)
			}
		}
	}

	// Decode input into canonical items
	var canonicalItems []lipapi.Item
	if jsonpresence.IsPresentNonNullJSON(param.Input) {
		trimmed := bytes.TrimSpace(param.Input)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// Input is a string shortcut
			var textInput string
			if err := json.Unmarshal(trimmed, &textInput); err != nil {
				return nil, lipapi.Call{}, fmt.Errorf("%w: invalid string input", ErrDecodeFailed)
			}
			canonicalItems = []lipapi.Item{
				{
					Kind:   lipapi.ItemKindMessage,
					Status: lipapi.ItemStatusCompleted,
					Role:   lipapi.RoleUser,
					Content: []lipapi.ContentPart{
						{
							Kind: lipapi.ContentPartText,
							Text: textInput,
						},
					},
				},
			}
		} else if len(trimmed) > 0 && trimmed[0] == '[' {
			// Input is an item array
			var wireItems []WireItem
			if err := json.Unmarshal(trimmed, &wireItems); err != nil {
				return nil, lipapi.Call{}, fmt.Errorf("%w: invalid item array input: %v", ErrDecodeFailed, err)
			}
			if err := ValidateItemCount(len(wireItems), limits); err != nil {
				return nil, lipapi.Call{}, err
			}
			continuationRefs := 0
			for _, w := range wireItems {
				if w.Type == "item_reference" {
					continuationRefs++
				}
			}
			if err := ValidateContinuationRefCount(continuationRefs, limits); err != nil {
				return nil, lipapi.Call{}, err
			}
			for i, w := range wireItems {
				item, err := DecodeItem(w, limits)
				if err != nil {
					return nil, lipapi.Call{}, fmt.Errorf("item[%d]: %w", i, err)
				}
				canonicalItems = append(canonicalItems, item)
			}
		} else {
			return nil, lipapi.Call{}, fmt.Errorf("%w: input must be string or item array", ErrDecodeFailed)
		}
	} else {
		// Input absent/null; allowed only if previous_response_id is set. Keep a
		// non-nil empty slice so the canonical call remains item-authoritative
		// and its tools/options still undergo Validate below.
		if param.PreviousResponseID == nil || strings.TrimSpace(*param.PreviousResponseID) == "" {
			return nil, lipapi.Call{}, fmt.Errorf("%w: input is required when previous_response_id is absent", ErrDecodeFailed)
		}
		if err := ValidateContinuationRef(strings.TrimSpace(*param.PreviousResponseID), limits); err != nil {
			return nil, lipapi.Call{}, err
		}
		canonicalItems = []lipapi.Item{}
	}
	if param.PreviousResponseID != nil && strings.TrimSpace(*param.PreviousResponseID) != "" {
		if err := ValidateContinuationRef(strings.TrimSpace(*param.PreviousResponseID), limits); err != nil {
			return nil, lipapi.Call{}, err
		}
	}

	// Decode tools
	var canonicalTools []lipapi.ToolDef
	for i, wt := range param.Tools {
		if wt.Type != "function" {
			return nil, lipapi.Call{}, fmt.Errorf("%w: tool[%d] has unsupported type %q", ErrDecodeFailed, i, wt.Type)
		}
		if wt.Name == "" {
			return nil, lipapi.Call{}, fmt.Errorf("%w: tool[%d] missing name", ErrDecodeFailed, i)
		}
		if err := ValidateSchemaSize(len(wt.Parameters), limits); err != nil {
			return nil, lipapi.Call{}, fmt.Errorf("tool[%d]: %w", i, err)
		}
		canonicalTools = append(canonicalTools, lipapi.ToolDef{
			Name:        wt.Name,
			Description: wt.Description,
			Parameters:  cloneBytes(wt.Parameters),
		})
	}

	// Decode tool_choice
	var toolChoice lipapi.ToolChoice
	if jsonpresence.IsPresentNonNullJSON(param.ToolChoice) {
		tc, err := decodeToolChoice(param.ToolChoice)
		if err != nil {
			return nil, lipapi.Call{}, err
		}
		toolChoice = tc
	}

	// Decode GenerationOptions
	options := lipapi.GenerationOptions{
		Temperature:       param.Temperature,
		TopP:              param.TopP,
		MaxOutputTokens:   param.MaxOutputTokens,
		ParallelToolCalls: param.ParallelToolCalls,
	}

	call := lipapi.Call{
		Items:              canonicalItems,
		PreviousResponseID: strings.TrimSpace(stringValue(param.PreviousResponseID)),
		Tools:              canonicalTools,
		ToolChoice:         toolChoice,
		Options:            options,
		Extensions:         extensions,
	}

	if err := call.Validate(); err != nil {
		return nil, lipapi.Call{}, fmt.Errorf("%w: canonical call validation failed: %w", ErrDecodeFailed, err)
	}

	return &param, call, nil
}

// decodeToolChoice decodes wire tool_choice JSON into lipapi.ToolChoice.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func decodeToolChoice(raw []byte) (lipapi.ToolChoice, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return lipapi.ToolChoice{}, nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return lipapi.ToolChoice{}, fmt.Errorf("%w: invalid string tool_choice", ErrDecodeFailed)
		}
		switch s {
		case "auto":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
		case "none":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
		case "required":
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
		default:
			return lipapi.ToolChoice{}, fmt.Errorf("%w: unknown string tool_choice %q", ErrDecodeFailed, s)
		}
	}

	if trimmed[0] == '{' {
		var rawObj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &rawObj); err != nil {
			return lipapi.ToolChoice{}, fmt.Errorf("%w: invalid object tool_choice", ErrDecodeFailed)
		}

		typeVal := ""
		if t, ok := rawObj["type"]; ok {
			if err := json.Unmarshal(t, &typeVal); err != nil {
				return lipapi.ToolChoice{}, fmt.Errorf("%w: invalid object tool_choice type", ErrDecodeFailed)
			}
		}

		if typeVal == "function" {
			var funcObj struct {
				Name string `json:"name"`
			}
			if fn, ok := rawObj["function"]; ok {
				if err := json.Unmarshal(fn, &funcObj); err != nil {
					return lipapi.ToolChoice{}, fmt.Errorf("%w: invalid function tool_choice object", ErrDecodeFailed)
				}
			}
			if funcObj.Name == "" {
				return lipapi.ToolChoice{}, fmt.Errorf("%w: invalid function tool_choice object missing name", ErrDecodeFailed)
			}
			return lipapi.ToolChoice{
				Mode: lipapi.ToolChoiceRequired,
				Name: funcObj.Name,
			}, nil
		}

		if typeVal == "allowed_tools" {
			return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
		}

		return lipapi.ToolChoice{}, fmt.Errorf("%w: unknown object tool_choice type %q", ErrDecodeFailed, typeVal)
	}

	return lipapi.ToolChoice{}, fmt.Errorf("%w: invalid tool_choice format", ErrDecodeFailed)
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
		if jsonpresence.IsPresentNonNullJSON(wire.Reasoning) {
			var reasStr string
			if err := json.Unmarshal(wire.Reasoning, &reasStr); err == nil {
				rItem.Reasoning.Text = reasStr
			} else {
				trimmed := bytes.TrimSpace(wire.Reasoning)
				if json.Valid(trimmed) {
					return lipapi.Item{}, fmt.Errorf("%w: reasoning cannot be a non-string JSON primitive", ErrDecodeFailed)
				}
				if len(trimmed) > 0 && (trimmed[0] == '"' || trimmed[0] == '{' || trimmed[0] == '[') {
					return lipapi.Item{}, fmt.Errorf("%w: reasoning must be a valid string", ErrDecodeFailed)
				}
				rItem.Reasoning.Text = string(trimmed)
			}
		} else if len(bytes.TrimSpace(wire.Content)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Content)) {
			return lipapi.Item{}, fmt.Errorf("%w: reasoning content cannot be null", ErrDecodeFailed)
		} else if jsonpresence.IsPresentNonNullJSON(wire.Content) {
			trimmed := bytes.TrimSpace(wire.Content)
			if len(trimmed) > 0 && trimmed[0] == '"' {
				var cStr string
				if err := json.Unmarshal(trimmed, &cStr); err != nil {
					return lipapi.Item{}, fmt.Errorf("%w: reasoning content is not a string union", ErrDecodeFailed)
				} else {
					rItem.Reasoning.Text = cStr
				}
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

// decodeContentParts parses wire content array into canonical []lipapi.ContentPart.
func decodeContentParts(raw []byte) ([]lipapi.ContentPart, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}

	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, fmt.Errorf("%w: content string shorthand must be a valid string", ErrDecodeFailed)
		}
		return []lipapi.ContentPart{{Kind: lipapi.ContentPartText, Text: text}}, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawParts); err != nil {
		return nil, fmt.Errorf("%w: content must be array of parts or a string", ErrDecodeFailed)
	}

	var parts []lipapi.ContentPart
	for i, rp := range rawParts {
		var wPart WireContentPart
		if err := json.Unmarshal(rp, &wPart); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}

		if !standardContentPartTypes[wPart.Type] && !isPrefixedType(wPart.Type) {
			return nil, fmt.Errorf("%w: content part %q", ErrUnknownDiscriminator, wPart.Type)
		}

		cp := lipapi.ContentPart{}
		switch wPart.Type {
		case "input_text", "output_text", "text":
			cp.Kind = lipapi.ContentPartText
			cp.Text = wPart.Text

		case "input_image":
			cp.Kind = lipapi.ContentPartImageRef
			if jsonpresence.IsPresentNonNullJSON(wPart.ImageURL) {
				trimmedImg := bytes.TrimSpace(wPart.ImageURL)
				if len(trimmedImg) > 0 && trimmedImg[0] == '"' {
					var imgStr string
					if err := json.Unmarshal(trimmedImg, &imgStr); err != nil {
						return nil, fmt.Errorf("content[%d]: %w: input_image image_url must be a valid string", i, ErrDecodeFailed)
					}
					cp.ImageRef = imgStr
				} else if len(trimmedImg) > 0 && trimmedImg[0] == '{' {
					var imgObj struct {
						URL string `json:"url"`
					}
					if err := json.Unmarshal(trimmedImg, &imgObj); err != nil {
						return nil, fmt.Errorf("content[%d]: %w: input_image image_url object is invalid", i, ErrDecodeFailed)
					}
					if imgObj.URL == "" {
						return nil, fmt.Errorf("content[%d]: %w: input_image image_url object is missing url", i, ErrDecodeFailed)
					}
					cp.ImageRef = imgObj.URL
				}
			}

		case "refusal":
			cp.Kind = lipapi.ContentPartRefusal
			cp.Refusal = wPart.Refusal

		default:
			// Vendor-prefixed custom content part mapped to text
			cp.Kind = lipapi.ContentPartText
			cp.Text = string(rp)
		}

		parts = append(parts, cp)
	}

	return parts, nil
}
