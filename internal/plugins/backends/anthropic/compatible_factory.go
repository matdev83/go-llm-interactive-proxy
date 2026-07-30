package anthropic

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatibleutil"
	"gopkg.in/yaml.v3"
)

// BuildCompatible constructs a built-in Anthropic-compatible backend from strict
// compatible-mode YAML for one configured runtime instance.
func BuildCompatible(instanceID, factoryKind string, n yaml.Node, upstream *http.Client) (execbackend.Backend, error) {
	cfg, err := config.DecodeCompatibleModeConfig(instanceID, factoryKind, n)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if err := pluginreg.ValidatePrefixSyntax(cfg.BackendPrefix); err != nil {
		return execbackend.Backend{}, err
	}
	ep, err := compatibleutil.ParseEndpoint(cfg)
	if err != nil {
		return execbackend.Backend{}, err
	}
	base := ep.BaseURL()
	modelsEndpoint, err := compatibleutil.AnthropicModelsEndpoint(ep)
	if err != nil {
		return execbackend.Backend{}, err
	}
	ek := compatibleutil.ResolveEnvAPIKeys(cfg.APIKeyEnvVarRoot)
	primaryKey := compatibleutil.FirstAPIKey(ek)
	client := upstream
	if client == nil {
		client = httpclient.Standard()
	}
	be := New(Config{
		BaseURL:            base,
		BackendPrefix:      cfg.BackendPrefix,
		APIKey:             primaryKey,
		APIKeys:            ek,
		HTTPClient:         client,
		CompatibleModeAuth: true,
		ModelsEndpoint:     modelsEndpoint,
	})
	be, err = compatibleutil.ApplyStaticModelInventory(be, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatibleutil.ApplyRuntimePolicy(be, cfg)
}

// LifecycleAnthropicCompatible is the standardplugins lifecycle entrypoint.
func LifecycleAnthropicCompatible(instanceID string, n yaml.Node, upstream *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatible(instanceID, "custom-anthropic-compatible", n, upstream)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}
