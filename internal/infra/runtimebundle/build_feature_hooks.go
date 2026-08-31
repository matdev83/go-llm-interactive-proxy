package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// BuildFeatureHooks merges enabled feature plugins into hook bus configuration.
func BuildFeatureHooks(reg *pluginreg.Registry, registrations []lipsdk.Registration) (hooks.Config, []lipplugin.Lifecycle, error) {
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, registrations)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return lipfeature.ProjectHookConfig(gen.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified), gen.Lifecycles, nil
}
