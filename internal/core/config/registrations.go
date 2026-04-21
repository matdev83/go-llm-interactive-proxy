package config

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// RegistrationsFromConfig maps YAML plugin rows to SDK registrations. The core only
// forwards opaque config nodes; it does not interpret plugin-private schema.
func RegistrationsFromConfig(cfg *Config) []lipsdk.Registration {
	if cfg == nil {
		return nil
	}

	var out []lipsdk.Registration

	for _, p := range cfg.Plugins.Frontends {
		out = append(out, lipsdk.Registration{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Kind:        lipsdk.PluginKindFrontend,
			Enabled:     p.Enabled,
			Config:      lipsdk.ConfigPayload{Node: p.Config},
		})
	}
	for _, p := range cfg.Plugins.Backends {
		out = append(out, lipsdk.Registration{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Kind:        lipsdk.PluginKindBackend,
			Enabled:     p.Enabled,
			Config:      lipsdk.ConfigPayload{Node: p.Config},
		})
	}
	for _, p := range cfg.Plugins.Features {
		out = append(out, lipsdk.Registration{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Kind:        lipsdk.PluginKindFeature,
			Enabled:     p.Enabled,
			Config:      lipsdk.ConfigPayload{Node: p.Config},
		})
	}

	return out
}
