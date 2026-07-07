package featurebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
	"gopkg.in/yaml.v3"
)

// FeatureFactory is aliased from pluginreg so this package can produce factories
// compatible with the registry without a hard dependency on the registry package
// beyond the type alias.
type FeatureFactory = pluginreg.FeatureFactory

// bundleFromHooksConfig copies hook chains and lifecycles from the legacy hook-only
// factory return into a versioned [lipfeature.FeatureBundle].
func bundleFromHooksConfig(cfg hooks.Config, lifes []lipplugin.Lifecycle) lipfeature.FeatureBundle {
	return lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		SubmitHooks:       append([]sdk.SubmitHook(nil), cfg.SubmitHooks...),
		RequestPartHooks:  append([]sdk.RequestPartHook(nil), cfg.RequestPartHooks...),
		ResponsePartHooks: append([]sdk.ResponsePartHook(nil), cfg.ResponsePartHooks...),
		ToolReactors:      append([]sdk.ToolReactor(nil), cfg.ToolReactors...),
		Lifecycles:        append([]lipplugin.Lifecycle(nil), lifes...),
	}
}

// FeatureFactoryFromHooks wraps a hook-only factory so it registers through the
// bundle composition path. ToolReactorErrorPolicy on hooks.Config is ignored here;
// the composition root continues to set the merged policy from runtime config.
func FeatureFactoryFromHooks(fn func(yaml.Node) (hooks.Config, []lipplugin.Lifecycle, error)) FeatureFactory {
	return func(n yaml.Node) (lipfeature.FeatureBundle, error) {
		cfg, lifes, err := fn(n)
		if err != nil {
			return lipfeature.FeatureBundle{}, err
		}
		return bundleFromHooksConfig(cfg, lifes), nil
	}
}
