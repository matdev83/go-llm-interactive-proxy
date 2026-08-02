package openresponsescompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	proto "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/protocols/openresponses"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// createRequest is the allowlisted create payload built by the generic
// OpenResponses backend. It mirrors the pinned profile request shape but
// intentionally exposes only the fields this backend forwards: proxy IDs,
// sessions, native refs, and arbitrary call extensions are never forwarded.
// Stream is set to true only for SSE transport attempts.
type createRequest struct {
	Model             string           `json:"model"`
	Input             []proto.WireItem `json:"input"`
	Tools             []proto.WireTool `json:"tools,omitempty"`
	ToolChoice        json.RawMessage  `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	TopP              *float64         `json:"top_p,omitempty"`
	MaxOutputTokens   *int             `json:"max_output_tokens,omitempty"`
	Text              json.RawMessage  `json:"text,omitempty"`
	Reasoning         json.RawMessage  `json:"reasoning,omitempty"`
	Stream            bool             `json:"stream,omitempty"`
}

// resolveModel returns the wire model for the request from the route candidate.
func resolveModel(cand routing.AttemptCandidate) string {
	return strings.TrimSpace(cand.Primary.Model)
}

// isPrefixedWireType reports whether a discriminator is a vendor-prefixed
// extension type (containing ':' or '/'), matching the pinned profile policy.
func isPrefixedWireType(t string) bool {
	return strings.Contains(t, ":") || strings.Contains(t, "/")
}

// representableContentPartKind reports whether a canonical content part can be
// encoded to the pinned profile by the production request codec without silent
// semantic loss (the codec falls back to text for unhandled part kinds).
func representableContentPartKind(k lipapi.ContentPartKind) bool {
	switch k {
	case lipapi.ContentPartText, lipapi.ContentPartImageRef, lipapi.ContentPartRefusal:
		return true
	}
	return false
}

// checkRepresentable validates that an item-authority call can be encoded to
// the pinned profile without silent semantic loss and within configured
// request limits. It must run before any HTTP round trip.
func checkRepresentable(id string, call lipapi.Call, limits RequestLimits) error {
	if len(call.Items) > limits.MaxItems {
		return fmt.Errorf("%s: %w", id, limitError("request_items", len(call.Items), limits.MaxItems))
	}
	var extensionBytes int
	for i, item := range call.Items {
		switch item.Kind {
		case lipapi.ItemKindMessage:
			if len(item.Content) > limits.MaxContentParts {
				return fmt.Errorf("%s: %w", id, limitError(fmt.Sprintf("request_items[%d].content_parts", i), len(item.Content), limits.MaxContentParts))
			}
			for j, cp := range item.Content {
				if !representableContentPartKind(cp.Kind) {
					return fmt.Errorf("%s: %w: message item %d content part %d kind %q cannot be encoded to the OpenResponses profile", id, ErrUnrepresentable, i, j, cp.Kind)
				}
			}
		case lipapi.ItemKindToolResult:
			if item.ToolResult == nil {
				return fmt.Errorf("%s: %w: tool result item %d is missing data", id, ErrUnrepresentable, i)
			}
			if len(item.ToolResult.Parts) > limits.MaxContentParts {
				return fmt.Errorf("%s: %w", id, limitError(fmt.Sprintf("request_items[%d].content_parts", i), len(item.ToolResult.Parts), limits.MaxContentParts))
			}
			for j, cp := range item.ToolResult.Parts {
				if !representableContentPartKind(cp.Kind) {
					return fmt.Errorf("%s: %w: tool result item %d part %d kind %q cannot be encoded to the OpenResponses profile", id, ErrUnrepresentable, i, j, cp.Kind)
				}
			}
		case lipapi.ItemKindReasoning:
			if item.Reasoning == nil || item.Reasoning.Reasoning == nil {
				return fmt.Errorf("%s: %w: reasoning item %d is missing its payload", id, ErrUnrepresentable, i)
			}
		case lipapi.ItemKindExtension:
			if item.Extension == nil {
				return fmt.Errorf("%s: %w: extension item %d is missing its payload", id, ErrUnrepresentable, i)
			}
			if !isPrefixedWireType(item.Extension.Type) {
				return fmt.Errorf("%s: %w: extension item %d type %q is not vendor-prefixed", id, ErrUnrepresentable, i, item.Extension.Type)
			}
			extensionBytes += len(item.Extension.Data)
		case lipapi.ItemKindToolCall:
			if item.ToolCall == nil {
				return fmt.Errorf("%s: %w: tool call item %d is missing its payload", id, ErrUnrepresentable, i)
			}
		case lipapi.ItemKindItemReference, lipapi.ItemKindCompaction:
			// Portable wired forms; EncodeItem rejects structurally invalid data.
		default:
			return fmt.Errorf("%s: %w: item %d kind %q is not representable", id, ErrUnrepresentable, i, item.Kind)
		}
	}
	if len(call.Tools) > limits.MaxTools {
		return fmt.Errorf("%s: %w", id, limitError("request_tools", len(call.Tools), limits.MaxTools))
	}
	if extensionBytes > limits.MaxExtensionBytes {
		return fmt.Errorf("%s: %w", id, limitError("request_extensions", extensionBytes, limits.MaxExtensionBytes))
	}
	return nil
}

// checkRequirements verifies the call's complete protocol requirements are
// satisfied by this instance before any upstream work. It mirrors the routing
// admission rule so unsupported semantics fail with zero round trips even when
// Open is invoked directly.
func checkRequirements(id string, call lipapi.Call, caps lipapi.BackendCaps, ds lipapi.DialectSupport) error {
	required := lipapi.DeriveProtocolRequirements(call)
	capList := make([]lipapi.Capability, 0, len(caps))
	for c := range caps {
		capList = append(capList, c)
	}
	supported := lipapi.ProtocolRequirements{
		Capabilities:       capList,
		ItemDialects:       ds.ItemDialects,
		ReasoningDialects:  ds.ReasoningDialects,
		CompactionDialects: ds.CompactionDialects,
		ExtensionTypes:     ds.ExtensionTypes,
	}
	if err := lipapi.MatchRequirements(required, supported, lipapi.ReasoningReplaySupport{}).Err(); err != nil {
		return fmt.Errorf("%s: %w: %v", id, ErrUnrepresentable, err)
	}
	return nil
}

// wireToolArguments normalizes canonical tool call arguments to the pinned
// profile's JSON-string wire form: a JSON string stays verbatim; any other
// valid JSON value is wrapped into a JSON string.
func wireToolArguments(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '"' {
		if json.Valid(trimmed) {
			return trimmed, nil
		}
		return nil, fmt.Errorf("%w: malformed function call arguments", ErrUnrepresentable)
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: function call arguments must be valid JSON", ErrUnrepresentable)
	}
	b, err := json.Marshal(string(trimmed))
	if err != nil {
		return nil, fmt.Errorf("%w: function call arguments: %v", ErrUnrepresentable, err)
	}
	return b, nil
}

func toolChoiceWire(tc lipapi.ToolChoice) (json.RawMessage, error) {
	if len(tc.AllowedTools) > 0 {
		refs := make([]proto.WireToolChoiceAllowedToolRef, 0, len(tc.AllowedTools))
		for _, name := range tc.AllowedTools {
			refs = append(refs, proto.WireToolChoiceAllowedToolRef{Type: "function", Name: name})
		}
		mode := "auto"
		switch tc.Mode {
		case lipapi.ToolChoiceNone:
			mode = "none"
		case lipapi.ToolChoiceAny:
			mode = "required"
		}
		b, err := json.Marshal(proto.WireToolChoiceAllowedTools{
			Type:  "allowed_tools",
			Tools: refs,
			Mode:  mode,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: marshal tool_choice: %v", ErrUnrepresentable, err)
		}
		return b, nil
	}
	switch tc.Mode {
	case lipapi.ToolChoiceAuto:
		return json.RawMessage(`"auto"`), nil
	case lipapi.ToolChoiceNone:
		return json.RawMessage(`"none"`), nil
	case lipapi.ToolChoiceAny:
		return json.RawMessage(`"required"`), nil
	case lipapi.ToolChoiceRequired:
		if tc.Name != "" {
			b, err := json.Marshal(proto.WireToolChoiceFunction{
				Type: "function",
				Name: tc.Name,
			})
			if err != nil {
				return nil, fmt.Errorf("%w: marshal tool_choice: %v", ErrUnrepresentable, err)
			}
			return b, nil
		}
		return json.RawMessage(`"required"`), nil
	default:
		return nil, nil
	}
}

// requestControls maps pinned generation controls to the wire request and
// rejects every nonzero option the pinned profile cannot represent exactly. No
// request control may be silently dropped.
func requestControls(o lipapi.GenerationOptions) (reasoning, text json.RawMessage, err error) {
	if o.Verbosity != "" {
		return nil, nil, fmt.Errorf("%w: verbosity is not representable on the pinned OpenResponses profile", ErrUnrepresentable)
	}
	if effort := strings.TrimSpace(o.ReasoningEffort); effort != "" {
		b, err := json.Marshal(struct {
			Effort string `json:"effort"`
		}{Effort: effort})
		if err != nil {
			return nil, nil, fmt.Errorf("%w: marshal reasoning: %v", ErrUnrepresentable, err)
		}
		reasoning = b
	}
	if mime := strings.ToLower(strings.TrimSpace(o.ResponseMIMEType)); mime != "" {
		format, err := textFormatForMIME(mime)
		if err != nil {
			return nil, nil, err
		}
		b, err := json.Marshal(struct {
			Format map[string]any `json:"format"`
		}{Format: format})
		if err != nil {
			return nil, nil, fmt.Errorf("%w: marshal text: %v", ErrUnrepresentable, err)
		}
		text = b
	}
	return reasoning, text, nil
}

// textFormatForMIME maps a supported canonical response MIME type to the exact
// pinned-profile text.format shape. MIME types the pinned schema cannot
// express with exact semantics are rejected.
func textFormatForMIME(mime string) (map[string]any, error) {
	switch mime {
	case "application/json":
		return map[string]any{"type": "json_object"}, nil
	case "text/plain":
		return map[string]any{"type": "text"}, nil
	default:
		return nil, fmt.Errorf("%w: response MIME type %q cannot be represented by the pinned OpenResponses text.format", ErrUnrepresentable, mime)
	}
}

// buildCreateRequest maps an item-authoritative canonical call to a
// schema-valid non-streaming create request body. It never forwards proxy IDs,
// sessions, native refs, or arbitrary call extension fields.
func buildCreateRequest(id string, spec BackendSpec, call lipapi.Call, cand routing.AttemptCandidate) ([]byte, error) {
	return buildCreateRequestBody(id, spec, call, cand, false)
}

// buildCreateRequestBody maps an item-authoritative canonical call to a
// schema-valid create request body. When stream is true the pinned profile's
// stream flag is set and the caller must negotiate SSE transport.
func buildCreateRequestBody(id string, spec BackendSpec, call lipapi.Call, cand routing.AttemptCandidate, stream bool) ([]byte, error) {
	model := resolveModel(cand)
	if model == "" {
		return nil, fmt.Errorf("%s: %w: model is required", id, ErrUnrepresentable)
	}
	if err := checkRepresentable(id, call, spec.RequestLimits); err != nil {
		return nil, err
	}

	wireItems := make([]proto.WireItem, 0, len(call.Items))
	for i, item := range call.Items {
		if item.Kind == lipapi.ItemKindToolCall {
			args, err := wireToolArguments(item.ToolCall.Arguments)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			cp := item
			tc := *item.ToolCall
			tc.Arguments = args
			cp.ToolCall = &tc
			item = cp
		}
		w, err := proto.EncodeItem(item)
		if err != nil {
			return nil, fmt.Errorf("%s: %w: item %d: %v", id, ErrUnrepresentable, i, err)
		}
		encoded, err := json.Marshal(w)
		if err != nil {
			return nil, fmt.Errorf("%s: %w: item %d: %v", id, ErrUnrepresentable, i, err)
		}
		if len(encoded) > spec.RequestLimits.MaxItemBytes {
			return nil, fmt.Errorf("%s: %w", id, limitError(fmt.Sprintf("request_items[%d]", i), len(encoded), spec.RequestLimits.MaxItemBytes))
		}
		wireItems = append(wireItems, w)
	}

	reasoning, text, err := requestControls(call.Options)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	req := createRequest{
		Model:             model,
		Input:             wireItems,
		Temperature:       call.Options.Temperature,
		TopP:              call.Options.TopP,
		MaxOutputTokens:   call.Options.MaxOutputTokens,
		ParallelToolCalls: call.Options.ParallelToolCalls,
		Text:              text,
		Reasoning:         reasoning,
		Stream:            stream,
	}
	if len(call.Tools) > 0 {
		tools := make([]proto.WireTool, 0, len(call.Tools))
		for _, t := range call.Tools {
			tools = append(tools, proto.WireTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  cloneBytes(t.Parameters),
			})
		}
		req.Tools = tools
		if tc, err := toolChoiceWire(call.ToolChoice); err != nil {
			return nil, fmt.Errorf("%s: %w: %v", id, ErrUnrepresentable, err)
		} else if tc != nil {
			req.ToolChoice = tc
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: marshal request: %v", id, ErrUnrepresentable, err)
	}
	if len(body) > proto.MaxRequestBytes {
		return nil, fmt.Errorf("%s: %w", id, limitError("request_size", len(body), proto.MaxRequestBytes))
	}
	return body, nil
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}
