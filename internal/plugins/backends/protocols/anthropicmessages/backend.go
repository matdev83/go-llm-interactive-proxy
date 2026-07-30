// Package anthropicmessages provides shared Anthropic Messages protocol execution.
package anthropicmessages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

type Config struct {
	BackendID          string
	BaseURL            string
	APIKey             string
	APIKeys            []string
	Credentials        []credpool.Credential
	HTTPClient         *http.Client
	SDKMaxRetries      *int
	RateLimitFallback  time.Duration
	ModelInventory     modelinventory.Provider
	ProviderCounter    accountingapp.ProviderCounter
	NormalizeRoles     bool
	NormalizeModel     func(string) string
	ThinkingFromEffort bool
	OmitToolChoice     bool
	// CompatibleModeAuth enables optional credentials for built-in compatible
	// modes: empty resolved keys omit x-api-key. Native Anthropic must leave false.
	CompatibleModeAuth bool
}

const defaultRateLimitFallback = 60 * time.Second

func NewBackend(cfg Config) execbackend.Backend {
	id := strings.TrimSpace(cfg.BackendID)
	if id == "" {
		id = "anthropic"
	}
	if err := checkcfg.RequireNonEmpty(id, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(id, err)
	}
	pool, noAuth, err := buildCompatibleOrRequiredPool(cfg)
	if err != nil {
		return newConfigErrorBackend(id, fmt.Errorf("%s: credentials: %w", id, err))
	}
	rateLimitFallback := cfg.RateLimitFallback
	if rateLimitFallback <= 0 {
		rateLimitFallback = defaultRateLimitFallback
	}
	backendCaps := defaultBackendCaps()
	if cfg.ThinkingFromEffort {
		backendCaps[lipapi.CapabilityReasoning] = struct{}{}
	}
	return execbackend.Backend{
		Caps:                                 backendCaps,
		ReplaySupport:                        ReplaySupport(),
		BackendPrefixes:                      []string{id},
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: execbackend.IgnoresClampViaCodexUnsupportedGenParams,
		ProviderCounter:                      cfg.ProviderCounter,
		ModelInventory:                       cfg.ModelInventory,
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			caps := ModelCapabilities(resolveModel(cand, call))
			if cfg.ThinkingFromEffort {
				caps[lipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, lipapi.ErrNilContext)
			}
			p, err := paramsForCall(&call, cand, cfg.NormalizeRoles, cfg.NormalizeModel)
			if cfg.OmitToolChoice {
				p.ToolChoice = anthropic.ToolChoiceUnionParam{}
			}
			if err != nil {
				return nil, err
			}
			if noAuth {
				cli := newSDKClientForSecret(cfg, "")
				stream := cli.Messages.NewStreaming(ctx, p)
				es := newMessageStream(stream, id, call.MaxPendingWireEvents)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewManagedPrependFirst(ev, es), nil
				}
				_ = es.Close()
				return nil, rerr
			}
			now := time.Now()
			for {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				cred, aerr := pool.Acquire(now, nil)
				if aerr != nil {
					if errors.Is(aerr, credpool.ErrNoUsableCredential) {
						return nil, lipapi.RecoverablePreOutputError(aerr)
					}
					return nil, fmt.Errorf("%s: %w", id, aerr)
				}
				cli := newSDKClientForSecret(cfg, cred.Secret)
				requestOpts := thinkingRequestOptions(call.Options.ReasoningEffort, cfg.ThinkingFromEffort)
				if cfg.ThinkingFromEffort && reasoningEffortEnablesThinking(call.Options.ReasoningEffort) {
					requestOpts = append(requestOpts, option.WithHeader("anthropic-beta", "interleaved-thinking-2025-05-14"))
				}
				stream := cli.Messages.NewStreaming(ctx, p, requestOpts...)
				es := newMessageStream(stream, id, call.MaxPendingWireEvents)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewManagedPrependFirst(ev, es), nil
				}
				_ = es.Close()
				kind, retryAfter := classifyAnthropicAPIError(rerr)
				now = time.Now()
				switch kind {
				case apiFailureAuthInvalid:
					pool.MarkAuthInvalid(cred.ID)
				case apiFailureRateLimited:
					until := credpool.CooldownFromRetryAfterOrFallback(retryAfter, now, rateLimitFallback)
					pool.MarkRateLimited(cred.ID, until)
				case apiFailureRetryable:
					// First Recv failed before the stream was returned: still pre-output,
					// so a transient upstream/transport failure is a core failover candidate.
					// rerr already carries this backend's ID prefix from the stream layer.
					return nil, lipapi.RecoverablePreOutputError(rerr)
				default:
					return nil, rerr
				}
			}
		},
	}
}

func buildCompatibleOrRequiredPool(cfg Config) (*credpool.Pool, bool, error) {
	if !cfg.CompatibleModeAuth {
		pool, err := credpool.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
		return pool, false, err
	}
	if len(cfg.Credentials) > 0 {
		pool, err := credpool.New(cfg.Credentials)
		return pool, false, err
	}
	secrets := make([]string, 0, 1+len(cfg.APIKeys))
	seen := make(map[string]struct{})
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		secrets = append(secrets, s)
	}
	add(cfg.APIKey)
	for _, k := range cfg.APIKeys {
		add(k)
	}
	if len(secrets) == 0 {
		return nil, true, nil
	}
	creds := make([]credpool.Credential, len(secrets))
	for i, s := range secrets {
		creds[i] = credpool.Credential{Secret: s}
	}
	pool, err := credpool.New(creds)
	return pool, false, err
}

func newConfigErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{id},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, call lipapi.Call, cand routing.AttemptCandidate) lipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
