package stdhttp

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// DefaultWireModel returns the default upstream model id for a bundled backend plugin id.
func DefaultWireModel(backendID string) string {
	switch backendID {
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

// DefaultRouteSelector returns the routing selector used by frontends when X-LIP-Route is absent.
func DefaultRouteSelector(cfg *config.Config) string {
	if cfg == nil {
		return "openai-responses:gpt-4o-mini"
	}
	if s := strings.TrimSpace(cfg.Routing.DefaultRoute); s != "" {
		return s
	}
	for _, p := range cfg.Plugins.Backends {
		if p.Enabled {
			return p.ID + ":" + DefaultWireModel(p.ID)
		}
	}
	return "openai-responses:gpt-4o-mini"
}
