package pluginreg

import "strings"

// DefaultWireModel returns the canonical default upstream model id for a bundled backend factory id.
//
// This mapping is intentionally separate from [*Registry]: the registry holds constructors and mounts;
// DefaultWireModel encodes standard-distribution routing defaults used when configs omit explicit
// route selectors (see routing.EffectiveDefaultRouteSelector). Alternate bundles may supply a
// different routing.WireModelForBackend via runtimebundle.BuildOptions.WireModel without changing
// registry factories. It must not be duplicated per frontend handler.
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
