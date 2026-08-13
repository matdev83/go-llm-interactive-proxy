package config

import "strings"

// DefaultFallbackWireModel is the compile-time model token used when synthesizing a
// default route selector and no WireModelForBackend mapping is available.
const DefaultFallbackWireModel = "gpt-4o-mini"

// DefaultFallbackRouteSelector is used only when wireModel is nil (tests / degenerate bootstrap).
const DefaultFallbackRouteSelector = "openai-responses:" + DefaultFallbackWireModel

// WireModelForBackend resolves the default model token for a backend factory id (plugin kind)
// when synthesizing a fallback route selector. Supplied at composition time (typically
// standardplugins.DefaultWireModel) so policy stays independent of HTTP mounting.
type WireModelForBackend func(factoryID string) string

// EffectiveDefaultRouteSelector returns the selector used when clients omit explicit routing
// (e.g. X-LIP-Route). Single implementation for the proxy; routing alias validation stays in
// package routing (see routing.ValidateModelAliasesConfig).
//
// Resolution order:
//  1. cfg.Routing.DefaultRoute when non-empty after trim
//  2. first enabled backend row in cfg.Plugins.Backends, as "<instance_id>:<wireModel(factory_id)>"
//  3. compile-time fallback "openai-responses:<wireModel(openai-responses)>" when wireModel is set
//  4. literal "openai-responses:gpt-4o-mini" only if wireModel is nil (tests / degenerate bootstrap)
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
			model := DefaultFallbackWireModel
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
			m = DefaultFallbackWireModel
		}
		return "openai-responses:" + m
	}
	return DefaultFallbackRouteSelector
}
