package backendplugin

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

const toolChoiceRequiredPrefix = "required:"

// ToolChoiceToWire maps canonical tool choice into the plugin ABI optional string field.
// Default auto with no named tool is represented as absent (nil).
func ToolChoiceToWire(tc lipapi.ToolChoice) *string {
	mode := tc.Mode
	if mode == "" {
		mode = lipapi.ToolChoiceAuto
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
	if strings.HasPrefix(raw, "auto:") {
		return lipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice auto name is not supported", ErrInvalidInvocation)
	}
	return lipapi.ToolChoice{}, fmt.Errorf("%w: unknown tool_choice %q", ErrInvalidInvocation, raw)
}

func validateToolChoiceWire(s *string) error {
	_, err := ToolChoiceFromWire(s)
	return err
}
