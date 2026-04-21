package config

import (
	"strings"

	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
)

// ParseToolReactorErrorPolicy maps YAML values to the stable hook-bus policy.
func ParseToolReactorErrorPolicy(s string) sdk.ToolReactorErrorPolicy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fail_closed", "fail-closed", "closed":
		return sdk.ToolReactorErrorsFailClosed
	case "swallow_event", "swallow-event", "swallow":
		return sdk.ToolReactorErrorsSwallowEvent
	default:
		return sdk.ToolReactorErrorsFailOpen
	}
}
