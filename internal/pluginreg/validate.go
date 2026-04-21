package pluginreg

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// ValidateBundledFactories ensures every mandatory plugin id has a registered factory
// for its kind. Call from the composition root after init-time registration.
func ValidateBundledFactories(required []lipsdk.Requirement) error {
	for _, r := range required {
		switch r.Kind {
		case lipsdk.PluginKindBackend:
			if _, err := lookupBackend(r.ID); err != nil {
				return err
			}
		case lipsdk.PluginKindFrontend:
			if _, err := lookupFrontend(r.ID); err != nil {
				return err
			}
		case lipsdk.PluginKindFeature:
			if _, err := lookupFeature(r.ID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("pluginreg: unknown plugin kind %q for id %q", r.Kind, r.ID)
		}
	}
	return nil
}

func lookupBackend(id string) (BackendFactory, error) {
	mu.RLock()
	defer mu.RUnlock()
	fn, ok := backends[id]
	if !ok {
		return nil, fmt.Errorf("pluginreg: mandatory backend %q has no registered factory", id)
	}
	return fn, nil
}

func lookupFrontend(id string) (FrontendMount, error) {
	mu.RLock()
	defer mu.RUnlock()
	fn, ok := frontends[id]
	if !ok {
		return nil, fmt.Errorf("pluginreg: mandatory frontend %q has no registered factory", id)
	}
	return fn, nil
}

func lookupFeature(id string) (FeatureFactory, error) {
	mu.RLock()
	defer mu.RUnlock()
	fn, ok := features[id]
	if !ok {
		return nil, fmt.Errorf("pluginreg: mandatory feature %q has no registered factory", id)
	}
	return fn, nil
}
