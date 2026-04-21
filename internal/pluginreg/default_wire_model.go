package pluginreg

import "strings"

// DefaultWireModel returns the canonical default upstream model id for a bundled backend
// plugin id. This is registry-owned composition metadata used when building fallback route
// selectors (see routing.EffectiveDefaultRouteSelector); it must not be duplicated per frontend handler.
func DefaultWireModel(backendID string) string {
	switch strings.TrimSpace(backendID) {
	case "openai-responses", "openai-legacy":
		return "gpt-4o-mini"
	case "anthropic":
		return "claude-3-5-haiku-20241022"
	case "gemini":
		return "gemini-2.0-flash"
	case "bedrock":
		return "anthropic.claude-3-haiku-20240307-v1:0"
	case "acp":
		return "agent"
	default:
		return "model"
	}
}
