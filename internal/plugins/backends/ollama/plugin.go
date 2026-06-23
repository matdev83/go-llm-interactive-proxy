package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicaps"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicompat"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/openrouterwire"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3/option"
)

const rateLimitFallback = 60 * time.Second

func New(cfg Config) execbackend.Backend {
	return newBackend(ID, cfg, backendModeLocal)
}

func NewCloud(cfg Config) execbackend.Backend {
	return newBackend(CloudID, cfg, backendModeCloud)
}

func newBackend(id string, cfg Config, mode backendMode) execbackend.Backend {
	cfg = ApplyDefaults(cfg)
	apiKey, apiKeys, credentials := EffectiveCredentials(cfg)

	nativeRoot := NativeRootFromBaseURL(cfg.BaseURL)
	responsesEnabled := resolveResponsesEnabled(cfg, nativeRoot, cfg.HTTPClient)
	resolveModelFn := resolveModelForMode(mode)

	inventory := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:     cfg.BaseURL,
		NativeRoot:  nativeRoot,
		APIKey:      apiKey,
		APIKeys:     apiKeys,
		Credentials: credpool.Secrets(credentials),
		HTTPClient:  cfg.HTTPClient,
		Discovery:   cfg.Discovery,
		Mode:        mode,
	})

	transportCaps := buildTransportCaps(responsesEnabled)

	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                id,
		BaseURL:           cfg.BaseURL,
		APIKey:            apiKey,
		APIKeys:           apiKeys,
		Credentials:       credentials,
		HTTPClient:        cfg.HTTPClient,
		SDKMaxRetries:     cfg.SDKMaxRetries,
		RateLimitFallback: rateLimitFallback,
		RequestOptions: func(call lipapi.Call) []option.RequestOption {
			return requestOptions(call)
		},
		ResolveModel: resolveModelFn,
		Inventory:    inventory,
		ResolveFlavor: func(call lipapi.Call) openaicompat.Flavor {
			if resolveFlavor(call) == openrouterwire.FlavorResponses {
				return openaicompat.FlavorResponses
			}
			return openaicompat.FlavorChat
		},
	})

	be.TransportCaps = transportCaps
	be.ResolveTransportCaps = func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendTransportCaps {
		return transportCaps
	}
	be.ResolveCaps = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
		model := nativeModelID(mode, resolveModelFn(cand, call))
		if model == "" {
			return openaicaps.ForHostedModel("")
		}
		if caps, ok := inventory.LookupCapsForNative(model); ok {
			return caps
		}
		if DiscoveryCapabilities(cfg.Discovery) {
			return inventory.ProbeCapsForNative(ctx, model)
		}
		return defaultModelCaps()
	}

	innerOpen := be.Open
	be.Open = func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		if resolveFlavor(call) == openrouterwire.FlavorResponses && !responsesEnabled {
			return nil, fmt.Errorf("ollama: responses API is not available (disabled or Ollama version < 0.13.3)")
		}
		if native := nativeModelID(mode, resolveModelFn(cand, call)); native != "" {
			cand.Primary.Model = native
		}
		return innerOpen(ctx, call, cand)
	}

	return be
}

func resolveResponsesEnabled(cfg Config, nativeRoot string, client *http.Client) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.ResponsesAPI)) {
	case "enabled":
		return true
	case "disabled":
		return false
	default:
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Discovery.Timeout)
		defer cancel()
		version, err := fetchVersion(ctx, client, nativeRoot)
		if err != nil {
			return false
		}
		return VersionSupportsResponses(version)
	}
}

func fetchVersion(ctx context.Context, client *http.Client, nativeRoot string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(strings.TrimSpace(nativeRoot), "/") + "/api/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("version HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	version := strings.TrimSpace(payload.Version)
	if version == "" {
		return "", fmt.Errorf("empty version")
	}
	return version, nil
}

func buildTransportCaps(responsesEnabled bool) lipapi.BackendTransportCaps {
	entries := []lipapi.OperationTransportSupport{
		{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		},
	}
	if responsesEnabled {
		entries = append(entries, lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIResponses,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
		})
	}
	return lipapi.NewBackendTransportCaps(entries...)
}
