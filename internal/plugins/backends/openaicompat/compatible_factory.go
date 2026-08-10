package openaicompat

import (
	"net/http"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/compatibleutil"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
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
	return BuildCompatibleWithHeaders(instanceID, factoryKind, n, upstream, flavor, transportCaps, nil)
}

// BuildCompatibleWithHeaders is the profile composition seam for the bounded
// static-header subset. Custom-compatible YAML remains on BuildCompatible and
// cannot inject arbitrary headers.
func BuildCompatibleWithHeaders(
	instanceID, factoryKind string,
	n yaml.Node,
	upstream *http.Client,
	flavor Flavor,
	transportCaps lipapi.BackendTransportCaps,
	headers map[string]string,
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
		RequestOptions:     staticHeaderOptions(headers),
	})
	be.TransportCaps = transportCaps
	be, err = compatibleutil.ApplyStaticModelInventory(be, cfg.Models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	return compatibleutil.ApplyRuntimePolicy(be, cfg)
}

func staticHeaderOptions(headers map[string]string) func(lipapi.Call) []option.RequestOption {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return func(lipapi.Call) []option.RequestOption {
		out := make([]option.RequestOption, 0, len(keys))
		for _, key := range keys {
			out = append(out, option.WithHeader(key, headers[key]))
		}
		return out
	}
}

// CompatibleTransportCaps exposes the existing family transport contract to
// profile composition without exposing a factory or provider registration.
func CompatibleTransportCaps(flavor Flavor) lipapi.BackendTransportCaps {
	if flavor == FlavorResponses {
		return customOpenAIResponsesTransportCaps()
	}
	return customOpenAILegacyTransportCaps()
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
