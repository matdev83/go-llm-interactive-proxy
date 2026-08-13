package openresponsescompat

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatmode"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

// Build constructs one generic OpenResponses backend instance from strict
// compatible-mode YAML. Credentials are resolved from the environment lazily at
// request time; static inventory is loaded from config/file only. upstream is
// the shared outbound HTTP client; when nil, [httpclient.Standard] is used.
func Build(instanceID string, n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	cfg, err := DecodeConfig(instanceID, ID, n)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := pluginreg.ValidatePrefixSyntax(cfg.BackendPrefix); err != nil {
		return execbackend.Backend{}, err
	}
	if _, err := validateBaseURL(cfg.BaseURL); err != nil {
		return execbackend.Backend{}, err
	}
	keys := compatmode.ResolveEnvAPIKeys(cfg.APIKeyEnvVarRoot)
	apiKey := compatmode.FirstAPIKey(keys)
	client := upstream
	if client == nil {
		client = httpclient.Standard()
	}

	var base execbackend.Backend
	base, err = compatmode.ApplyStaticModelInventory(base, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}

	codec, err := NewCodecOptions(cfg.Profile, ID, "")
	if err != nil {
		return execbackend.Backend{}, err
	}

	return NewBackend(BackendSpec{
		ID:               cfg.BackendPrefix,
		APIKeyEnvVarRoot: cfg.APIKeyEnvVarRoot,
		APIKey:           apiKey,
		APIKeys:          keys,
		BaseURL:          cfg.BaseURL,
		HTTPClient:       client,
		RequestLimits:    cfg.RequestLimits,
		ResponseLimits:   cfg.ResponseLimits,
		Caps:             lipapi.NewBackendCaps(cfg.Capabilities...),
		DialectSupport:   dialectSupportFromConfig(cfg),
		Inventory:        base.ModelInventory,
		Codec:            codec,
	}), nil
}

// LifecycleOpenResponsesCompatible is the standardplugins lifecycle entrypoint
// for the generic OpenResponses built-in-compatible backend kind.
func LifecycleOpenResponsesCompatible(instanceID string, n yaml.Node, upstream *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := Build(instanceID, n, upstream)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}

func dialectSupportFromConfig(cfg Config) lipapi.DialectSupport {
	out := lipapi.DialectSupport{}
	for _, d := range cfg.Dialects.Item {
		out.ItemDialects = append(out.ItemDialects, lipapi.DialectRequirement{
			Kind:        "item",
			Dialect:     d.Dialect,
			Implementor: d.Implementor,
		})
	}
	for _, d := range cfg.Dialects.Reasoning {
		out.ReasoningDialects = append(out.ReasoningDialects, lipapi.DialectRequirement{
			Kind:        "reasoning",
			Dialect:     d.Dialect,
			Implementor: d.Implementor,
		})
	}
	for _, d := range cfg.Dialects.Compaction {
		out.CompactionDialects = append(out.CompactionDialects, lipapi.DialectRequirement{
			Kind:        "compaction",
			Dialect:     d.Dialect,
			Implementor: d.Implementor,
		})
	}
	for _, e := range cfg.Dialects.Extensions {
		out.ExtensionTypes = append(out.ExtensionTypes, lipapi.ExtensionRequirement{
			Namespace:   e.Namespace,
			Type:        e.Type,
			Implementor: e.Implementor,
		})
	}
	return lipapi.NormalizeDialectSupport(out)
}
