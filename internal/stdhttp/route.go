package stdhttp

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// DefaultRouteSelector returns the routing selector used by frontends when X-LIP-Route is absent.
// It delegates to routing.EffectiveDefaultRouteSelector with pluginreg.DefaultWireModel so default
// models are registry-owned, not duplicated in frontend handlers.
func DefaultRouteSelector(cfg *config.Config) string {
	return routing.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
}
