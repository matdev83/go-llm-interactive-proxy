package stdhttp

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// DefaultRouteSelector returns the routing selector used by frontends when X-LIP-Route is absent.
// It delegates to config.EffectiveDefaultRouteSelector with pluginreg.DefaultWireModel so default
// models are registry-owned, not duplicated in frontend handlers.
//
// Prefer using [runtimebundle.Built.EffectiveDefaultRoute] from [runtimebundle.Build] so HTTP wiring
// shares the same wire-model selection as the executor (including optional [runtimebundle.BuildOptions.WireModel]).
func DefaultRouteSelector(cfg *config.Config) string {
	return config.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
}
