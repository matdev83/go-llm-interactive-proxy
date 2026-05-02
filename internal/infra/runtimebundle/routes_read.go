package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// RoutesSnapshot is a config-derived operator read model (no provider I/O).
type RoutesSnapshot struct {
	EffectiveDefaultRoute string         `json:"effective_default_route"`
	Backends              []RouteBackend `json:"backends"`
	ModelAliases          []RouteAlias   `json:"model_aliases"`
	// CredentialPosture is derived from enabled backend factory ids only: all_local_stub when every
	// enabled backend uses factory kind local-stub; live_provider when any enabled backend is not
	// local-stub; no_enabled_backends when there are no enabled backend rows.
	CredentialPosture string `json:"credential_posture"`
}

// RouteBackend is one configured backend row for route inspection.
type RouteBackend struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

// RouteAlias is one model alias rule from configuration.
type RouteAlias struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

// RoutesSnapshotFrom builds a deterministic route read model from config and the standard registry
// wire model (same default-route resolution as serving).
func RoutesSnapshotFrom(cfg *config.Config, reg *pluginreg.Registry) (RoutesSnapshot, error) {
	if cfg == nil {
		return RoutesSnapshot{}, fmt.Errorf("runtimebundle: nil config")
	}
	if reg == nil {
		return RoutesSnapshot{}, fmt.Errorf("runtimebundle: nil registry")
	}
	raw := config.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	aliasResolver, err := routing.NewAliasResolver(routing.ModelAliasRulesFromConfig(cfg))
	if err != nil {
		return RoutesSnapshot{}, fmt.Errorf("runtimebundle: model_aliases: %w", err)
	}
	out := RoutesSnapshot{
		EffectiveDefaultRoute: aliasResolver.Resolve(raw),
		Backends:              make([]RouteBackend, 0, len(cfg.Plugins.Backends)),
		ModelAliases:          make([]RouteAlias, 0, len(cfg.ModelAliases)),
		CredentialPosture:     credentialPostureFromBackends(cfg),
	}
	for _, b := range cfg.Plugins.Backends {
		out.Backends = append(out.Backends, RouteBackend{
			ID:      b.InstanceID(),
			Kind:    b.FactoryID(),
			Enabled: b.Enabled,
		})
	}
	for _, a := range cfg.ModelAliases {
		out.ModelAliases = append(out.ModelAliases, RouteAlias{
			Pattern:     a.Pattern,
			Replacement: a.Replacement,
		})
	}
	return out, nil
}

func credentialPostureFromBackends(cfg *config.Config) string {
	any := false
	for _, b := range cfg.Plugins.Backends {
		if !b.Enabled {
			continue
		}
		any = true
		k := strings.ToLower(strings.TrimSpace(b.FactoryID()))
		if k != "local-stub" {
			return "live_provider"
		}
	}
	if !any {
		return "no_enabled_backends"
	}
	return "all_local_stub"
}
