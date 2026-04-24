package config

import "strings"

type WireModelForBackend func(factoryID string) string

func EffectiveDefaultRouteSelector(cfg *Config, wireModel WireModelForBackend) string {
	if cfg != nil {
		if s := strings.TrimSpace(cfg.Routing.DefaultRoute); s != "" {
			return s
		}
		for _, p := range cfg.Plugins.Backends {
			if !p.Enabled {
				continue
			}
			instance := p.InstanceID()
			if instance == "" {
				continue
			}
			model := "gpt-4o-mini"
			if wireModel != nil {
				model = strings.TrimSpace(wireModel(p.FactoryID()))
				if model == "" {
					model = "model"
				}
			}
			return instance + ":" + model
		}
	}
	if wireModel != nil {
		m := strings.TrimSpace(wireModel("openai-responses"))
		if m == "" {
			m = "gpt-4o-mini"
		}
		return "openai-responses:" + m
	}
	return "openai-responses:gpt-4o-mini"
}
