package backendplugin

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const (
	toolChoiceRequiredPrefix = "required:"
	toolChoiceAllowedPrefix  = "allowed:"
)

// ValidateToolChoiceABI reports whether the canonical tool choice can be
// represented by the plugin ABI optional string without semantic loss. The
// allowed_tools subset is encoded as "allowed:<mode>:<n1>,<n2>", so a tool name
// containing a comma cannot round-trip and is rejected explicitly. Mode
// required combined with a subset is rejected too: the ABI decoder accepts only
// auto/none/any as the allowed mode, so the encoder could otherwise emit an
// "allowed:required:..." string the decoder rejects.
func ValidateToolChoiceABI(tc lipapi.ToolChoice) error {
	if tc.Mode == lipapi.ToolChoiceRequired && len(tc.AllowedTools) > 0 {
		return fmt.Errorf("%w: tool_choice mode required is incompatible with allowed tools", ErrInvalidInvocation)
	}
	for _, name := range tc.AllowedTools {
		if strings.Contains(name, ",") {
			return fmt.Errorf("%w: allowed tool name %q contains a comma and cannot be represented by the plugin ABI", ErrInvalidInvocation, name)
		}
	}
	return nil
}

// ToolChoiceToWire maps canonical tool choice into the plugin ABI optional string field.
// Default auto with no named tool is represented as absent (nil). The
// allowed_tools subset is encoded as "allowed:<mode>:<n1>,<n2>".
func ToolChoiceToWire(tc lipapi.ToolChoice) *string {
	mode := tc.Mode
	if mode == "" {
		mode = lipapi.ToolChoiceAuto
	}
	if len(tc.AllowedTools) > 0 {
		s := toolChoiceAllowedPrefix + string(mode) + ":" + strings.Join(tc.AllowedTools, ",")
		return &s
	}
	switch mode {
	case lipapi.ToolChoiceAuto:
		return nil
	case lipapi.ToolChoiceNone:
		s := string(lipapi.ToolChoiceNone)
		return &s
	case lipapi.ToolChoiceAny:
		s := string(lipapi.ToolChoiceAny)
		return &s
	case lipapi.ToolChoiceRequired:
		if tc.Name != "" {
			s := toolChoiceRequiredPrefix + tc.Name
			return &s
		}
		s := string(lipapi.ToolChoiceRequired)
		return &s
	default:
		s := string(mode)
		return &s
	}
}

// ToolChoiceFromWire decodes the plugin ABI tool choice string into canonical form.
func ToolChoiceFromWire(s *string) (lipapi.ToolChoice, error) {
	if s == nil {
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	}
	raw := *s
	if raw == "" {
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	}
	if raw != strings.TrimSpace(raw) {
		return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice must not contain leading or trailing whitespace", ErrInvalidInvocation)
	}
	switch raw {
	case string(lipapi.ToolChoiceAuto):
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAuto}, nil
	case string(lipapi.ToolChoiceNone):
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceNone}, nil
	case string(lipapi.ToolChoiceAny):
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceAny}, nil
	case string(lipapi.ToolChoiceRequired):
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired}, nil
	}
	if strings.HasPrefix(raw, toolChoiceRequiredPrefix) {
		name := strings.TrimPrefix(raw, toolChoiceRequiredPrefix)
		if name == "" {
			return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice required name is empty", ErrInvalidInvocation)
		}
		if name != strings.TrimSpace(name) {
			return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice required name must not contain leading or trailing whitespace", ErrInvalidInvocation)
		}
		return lipapi.ToolChoice{Mode: lipapi.ToolChoiceRequired, Name: name}, nil
	}
	if strings.HasPrefix(raw, toolChoiceAllowedPrefix) {
		return toolChoiceFromAllowedWire(raw)
	}
	if strings.HasPrefix(raw, "auto:") {
		return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice auto name is not supported", ErrInvalidInvocation)
	}
	return lipapi.ToolChoice{}, fmt.Errorf("%w: unknown tool_choice %q", ErrInvalidInvocation, raw)
}

// toolChoiceFromAllowedWire decodes "allowed:<mode>:<n1>,<n2>" into canonical
// form. Mode must be one of the canonical auto/none/any values; names must be
// non-empty and whitespace-free. Colons inside a name are preserved (the first
// two segments are mode and the comma-joined name list).
func toolChoiceFromAllowedWire(raw string) (lipapi.ToolChoice, error) {
	body := strings.TrimPrefix(raw, toolChoiceAllowedPrefix)
	parts := strings.SplitN(body, ":", 2)
	if len(parts) != 2 {
		return lipapi.ToolChoice{}, fmt.Errorf("%w: malformed tool_choice %q", ErrInvalidInvocation, raw)
	}
	var mode lipapi.ToolChoiceMode
	switch parts[0] {
	case string(lipapi.ToolChoiceAuto):
		mode = lipapi.ToolChoiceAuto
	case string(lipapi.ToolChoiceNone):
		mode = lipapi.ToolChoiceNone
	case string(lipapi.ToolChoiceAny):
		mode = lipapi.ToolChoiceAny
	default:
		return lipapi.ToolChoice{}, fmt.Errorf("%w: unknown tool_choice allowed mode %q", ErrInvalidInvocation, parts[0])
	}
	namesStr := parts[1]
	if namesStr == "" || namesStr != strings.TrimSpace(namesStr) {
		return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice allowed tools must not be empty or whitespace-padded", ErrInvalidInvocation)
	}
	names := strings.Split(namesStr, ",")
	allowed := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || name != strings.TrimSpace(name) {
			return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice allowed tool name is empty or whitespace-padded", ErrInvalidInvocation)
		}
		allowed = append(allowed, name)
	}
	return lipapi.ToolChoice{Mode: mode, AllowedTools: allowed}, nil
}

func validateToolChoiceWire(s *string) error {
	_, err := ToolChoiceFromWire(s)
	return err
}
