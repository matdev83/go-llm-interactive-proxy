package pluginreg

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
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

func installBackends(reg *Registry, keys UpstreamAPIKeys) error {
	entries := []struct {
		ID      string
		Factory backendFactory
	}{
		{openairesponses.ID, func(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
			return backendOpenAIResponses(n, upstream, keys)
		}},
		{openailegacy.ID, func(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
			return backendOpenAILegacy(n, upstream, keys)
		}},
		{anthropic.ID, func(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
			return backendAnthropic(n, upstream, keys)
		}},
		{gemini.ID, func(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
			return backendGemini(n, upstream, keys)
		}},
		{bedrock.ID, func(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
			return backendBedrock(n, upstream)
		}},
		{acp.ID, func(n yaml.Node, upstream *http.Client) (runtime.Backend, error) {
			return backendACP(n, upstream)
		}},
	}
	for _, e := range entries {
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
// keys supplies default API key material when plugin YAML omits api_key (typically from
// [ResolveUpstreamAPIKeysFromEnv] at process startup); tests may pass a zero value.
func InstallStandardBundleOn(reg *Registry, keys UpstreamAPIKeys) error {
	if err := installBackends(reg, keys); err != nil {
		return err
	}
	if err := installFrontends(reg); err != nil {
		return err
	}
	return installFeatures(reg)
}

// InstallStandardBackendsOn registers only bundled backend factories on reg (minimal partial bundles).
func InstallStandardBackendsOn(reg *Registry, keys UpstreamAPIKeys) error {
	return installBackends(reg, keys)
}
