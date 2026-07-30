package openaicompat

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatibleutil"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

// BuildCompatible constructs a built-in OpenAI-family compatible backend from
// strict compatible-mode YAML for one configured runtime instance.
func BuildCompatible(
	instanceID, factoryKind string,
	n yaml.Node,
	upstream *http.Client,
	flavor Flavor,
	transportCaps lipapi.BackendTransportCaps,
) (execbackend.Backend, error) {
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
	modelsEndpoint, err := compatibleutil.OpenAIModelsEndpoint(ep)
	if err != nil {
		return execbackend.Backend{}, err
	}
	ek := compatibleutil.ResolveEnvAPIKeys(cfg.APIKeyEnvVarRoot)
	apiKey := compatibleutil.FirstAPIKey(ek)
	client := upstream
	if client == nil {
		client = httpclient.Standard()
	}
	inventory := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:            base,
		ModelsEndpoint:     modelsEndpoint,
		APIKey:             apiKey,
		APIKeys:            ek,
		HTTPClient:         client,
		CanonicalPrefix:    cfg.BackendPrefix,
		CompatibleModeAuth: true,
	}
	be := NewBackend(BackendSpec{
		ID:                 cfg.BackendPrefix,
		BaseURL:            base,
		APIKey:             apiKey,
		APIKeys:            ek,
		HTTPClient:         client,
		SDKMaxRetries:      sdkMaxRetriesOrDefault(nil),
		Inventory:          inventory,
		CompatibleModeAuth: true,
		ResolveFlavor:      func(lipapi.Call) Flavor { return flavor },
	})
	be.TransportCaps = transportCaps
	be, err = compatibleutil.ApplyStaticModelInventory(be, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatibleutil.ApplyRuntimePolicy(be, cfg)
}

func sdkMaxRetriesOrDefault(v *int) *int {
	if v != nil {
		return v
	}
	zero := 0
	return &zero
}

// LifecycleOpenAILegacyCompatible is the standardplugins lifecycle entrypoint.
func LifecycleOpenAILegacyCompatible(instanceID string, n yaml.Node, upstream *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatible(instanceID, "custom-openai-legacy-compatible", n, upstream, FlavorChat, customOpenAILegacyTransportCaps())
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}

// LifecycleOpenAIResponsesCompatible is the standardplugins lifecycle entrypoint.
func LifecycleOpenAIResponsesCompatible(instanceID string, n yaml.Node, upstream *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	be, err := BuildCompatible(instanceID, "custom-openai-responses-compatible", n, upstream, FlavorResponses, customOpenAIResponsesTransportCaps())
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	return pluginreg.BackendBuildResult{Backend: be}, nil
}

func customOpenAILegacyTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}

func customOpenAIResponsesTransportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(
		lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIResponses,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	)
}
