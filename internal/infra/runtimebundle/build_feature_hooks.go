package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// hooksConfigFromMerged builds a [hooks.Config] at the composition root from a
// [featurebundle.MergedFeatureSurface].
func hooksConfigFromMerged(m featurebundle.MergedFeatureSurface) hooks.Config {
	return hooks.Config{
		SubmitHooks:            m.SubmitHooks,
		RequestPartHooks:       m.RequestPartHooks,
		ResponsePartHooks:      m.ResponsePartHooks,
		ToolReactors:           m.ToolReactors,
		ToolReactorErrorPolicy: m.ToolReactorErrorPolicy,
	}
}

// BuildFeatureHooks merges enabled feature plugins into hook bus configuration (brownfield API).
// For the full surface including session openers and workspace resolvers, use [featurebundle.MergeFeatureSurface].
func BuildFeatureHooks(reg *pluginreg.Registry, registrations []lipsdk.Registration) (hooks.Config, []lipplugin.Lifecycle, error) {
	m, err := featurebundle.MergeFeatureSurface(reg, registrations)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return hooksConfigFromMerged(m), m.Lifecycles, nil
}
