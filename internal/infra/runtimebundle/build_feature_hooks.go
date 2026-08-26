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

// HooksConfigFromGenerated derives [hooks.Config] from [featurebundle.GeneratedMergeSurface].
func HooksConfigFromGenerated(g featurebundle.GeneratedMergeSurface, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return HooksConfigFromFrozen(g.Frozen, p)
}

// HooksConfigFromFrozen derives [hooks.Config] from [lipfeature.FrozenPlaneSet].
func HooksConfigFromFrozen(f lipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{SubmitHooks: lipfeature.Get(f, lipfeature.PlaneSubmitHooks), RequestPartHooks: lipfeature.Get(f, lipfeature.PlaneRequestPartHooks), ResponsePartHooks: lipfeature.Get(f, lipfeature.PlaneResponsePartHooks), ToolReactors: lipfeature.Get(f, lipfeature.PlaneToolReactors), ToolReactorErrorPolicy: p}
}

// BuildFeatureHooks merges enabled feature plugins into hook bus configuration.
func BuildFeatureHooks(reg *pluginreg.Registry, registrations []lipsdk.Registration) (hooks.Config, []lipplugin.Lifecycle, error) {
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, registrations)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return HooksConfigFromGenerated(gen, sdkhooks.ToolReactorErrorPolicyUnspecified), gen.Lifecycles, nil
}
