package openailegacy

import (
	"context"
	"fmt"
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/modeldiscover"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicaps"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicred"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// Config configures the legacy OpenAI Chat Completions backend connector (official SDK).
// BaseURL must include the API prefix (e.g. https://api.openai.com/v1).
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	// Integration tests that assert a single upstream attempt on 429/401 should use a pointer to 0.
	SDKMaxRetries    *int
	DefaultVerbosity lipapi.VerbosityLevel
}

const (
	DefaultBaseURL          = "https://api.openai.com/v1"
	openAIRateLimitFallback = credpool.DefaultRateLimitFallback
)

// New returns a runtime backend that invokes the OpenAI Chat Completions API using openai-go.
func New(cfg Config) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	if err := normalizeDefaultVerbosity(&cfg.DefaultVerbosity); err != nil {
		return newConfigErrorBackend(err)
	}
	pool, err := openaicred.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if err != nil {
		return newConfigErrorBackend(fmt.Errorf("%s: credentials: %w", ID, err))
	}
	prefixes := []string{ID}
	return execbackend.Backend{
		Caps:            openaicaps.HostedFull,
		ReplaySupport:   lipapi.ReasoningReplaySupport{},
		BackendPrefixes: prefixes,
		ModelInventory: modeldiscover.OpenAICompatibleModelsProvider{
			BaseURL:         cfg.BaseURL,
			APIKey:          cfg.APIKey,
			APIKeys:         cfg.APIKeys,
			Credentials:     credentialSecrets(cfg.Credentials),
			HTTPClient:      cfg.HTTPClient,
			CanonicalPrefix: "openai",
		},
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: execbackend.IgnoresClampViaCodexUnsupportedGenParams,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModelCompatibleReplay(resolveModel(cand, call), prefixes)
		},
		ResolveReplaySupport: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.ReasoningReplaySupport {
			return openaicaps.ResolveCompatibleReplaySupport(openaicaps.FlavorChat, resolveModel(cand, call), prefixes)
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, lipapi.ErrNilContext)
			}
			if call.Options.Verbosity == "" {
				call.Options.Verbosity = cfg.DefaultVerbosity
			}
			p, err := ParamsForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			return openaicred.ExecuteWithCredentialPool(ctx, ID, pool, openAIRateLimitFallback, func(ctx context.Context, cred credpool.Credential) (lipapi.ManagedEventStream, error) {
				cli := openaicred.NewClient(cfg.BaseURL, cred.Secret, cfg.HTTPClient, cfg.SDKMaxRetries)
				if call.Invocation.TransportMode == lipapi.TransportModeNonStreaming {
					comp, nerr := cli.Chat.Completions.New(ctx, p)
					if nerr != nil {
						return nil, nerr
					}
					return lipapi.NewFixedEventStream(CompletionEvents(*comp)), nil
				}
				raw := cli.Chat.Completions.NewStreaming(ctx, p)
				es := NewChatStream(raw, call.MaxPendingWireEvents)
				return streampeek.PeekFirst(ctx, es)
			})
		},
	}
}

func normalizeDefaultVerbosity(value *lipapi.VerbosityLevel) error {
	parsed, err := lipapi.ParseVerbosityLevel(string(*value))
	if err != nil {
		return fmt.Errorf("%s: default_verbosity: %w", ID, err)
	}
	*value = parsed
	return nil
}

func credentialSecrets(credentials []credpool.Credential) []string {
	if len(credentials) == 0 {
		return nil
	}
	out := make([]string, 0, len(credentials))
	for _, cred := range credentials {
		out = append(out, cred.Secret)
	}
	return out
}

func newConfigErrorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            openaicaps.HostedFull,
		BackendPrefixes: []string{ID},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
