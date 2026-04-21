package routing

import (
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
)

// WireModelForBackend resolves the default model token for a backend factory id (plugin kind)
// when synthesizing a fallback route selector. Supplied at composition time (typically
// pluginreg.DefaultWireModel) so routing policy stays independent of HTTP mounting.
type WireModelForBackend func(factoryID string) string

// EffectiveDefaultRouteSelector is the single documented source for the selector used when
// clients omit explicit routing headers or equivalent fields (e.g. X-LIP-Route).
//
// Resolution order:
//  1. cfg.Routing.DefaultRoute when non-empty after trim
//  2. first enabled backend row in cfg.Plugins.Backends, as "<instance_id>:<wireModel(factory_id)>"
//  3. compile-time fallback "openai-responses:<wireModel(openai-responses)>" when wireModel is set
//  4. literal "openai-responses:gpt-4o-mini" only if wireModel is nil (tests / degenerate bootstrap)
func EffectiveDefaultRouteSelector(cfg *config.Config, wireModel WireModelForBackend) string {
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
			model := "model"
			if wireModel != nil {
				model = strings.TrimSpace(wireModel(p.FactoryID()))
				if model == "" {
					model = "model"
				}
			} else {
				model = "gpt-4o-mini"
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
