package pluginreg

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/bedrock"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/partsnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refparts"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refsubmit"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/submitnoop"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolreactornoop"
	frontanthropic "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	frontgemini "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"gopkg.in/yaml.v3"
)

// standardBackendFactories is the explicit compile-time table of bundled backend factories (factory id → constructor).
var standardBackendFactories = []struct {
	ID      string
	Factory backendFactory
}{
	{openairesponses.ID, func(n yaml.Node, upstream *http.Client) (any, error) {
		return backendOpenAIResponses(n, upstream)
	}},
	{openailegacy.ID, func(n yaml.Node, upstream *http.Client) (any, error) {
		return backendOpenAILegacy(n, upstream)
	}},
	{anthropic.ID, func(n yaml.Node, upstream *http.Client) (any, error) {
		return backendAnthropic(n, upstream)
	}},
	{gemini.ID, func(n yaml.Node, upstream *http.Client) (any, error) {
		return backendGemini(n, upstream)
	}},
	{bedrock.ID, func(n yaml.Node, upstream *http.Client) (any, error) {
		return backendBedrock(n, upstream)
	}},
	{acp.ID, func(n yaml.Node, upstream *http.Client) (any, error) {
		return backendACP(n, upstream)
	}},
}

func installBackends(reg *Registry) error {
	for _, e := range standardBackendFactories {
		if err := reg.RegisterBackend(e.ID, e.Factory); err != nil {
			return err
		}
	}
	return nil
}

var standardFrontendMounts = []struct {
	ID    string
	Mount FrontendMount
}{
	{frontopenairesponses.ID, mountOpenAIResponses},
	{frontopenailegacy.ID, mountOpenAILegacy},
	{frontanthropic.ID, mountAnthropic},
	{frontgemini.ID, mountGemini},
}

func installFrontends(reg *Registry) error {
	for _, e := range standardFrontendMounts {
		if err := reg.RegisterFrontend(e.ID, e.Mount); err != nil {
			return err
		}
	}
	return nil
}

var standardFeatureFactories = []struct {
	ID      string
	Factory FeatureFactory
}{
	{submitnoop.ID, featureSubmitNoop},
	{partsnoop.ID, featurePartsNoop},
	{toolreactornoop.ID, featureToolReactorNoop},
	{refsubmit.ID, featureRefSubmit},
	{refparts.ID, featureRefParts},
	{reftool.ID, featureRefTool},
}

func installFeatures(reg *Registry) error {
	for _, e := range standardFeatureFactories {
		if err := reg.RegisterFeature(e.ID, e.Factory); err != nil {
			return err
		}
	}
	return nil
}

// InstallStandardBundleOn registers all standard bundled factories on reg (tests, alternate bundles).
func InstallStandardBundleOn(reg *Registry) error {
	if err := installBackends(reg); err != nil {
		return err
	}
	if err := installFrontends(reg); err != nil {
		return err
	}
	return installFeatures(reg)
}

// InstallStandardBackendsOn registers only bundled backend factories on reg (minimal partial bundles).
func InstallStandardBackendsOn(reg *Registry) error {
	return installBackends(reg)
}
