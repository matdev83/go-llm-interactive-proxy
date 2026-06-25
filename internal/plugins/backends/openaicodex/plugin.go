package openaicodex

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

var backendCaps = lipapi.NewBackendCaps(
	lipapi.CapabilityStreaming,
	lipapi.CapabilityTools,
	lipapi.CapabilityVision,
	lipapi.CapabilityDocuments,
	lipapi.CapabilityReasoning,
	lipapi.CapabilityParallelToolCalls,
)

type backendRuntime struct {
	mu    sync.Mutex
	cfg   Config
	oauth *accountStore
}

func New(cfg Config) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	rt := &backendRuntime{}
	resolved, store, err := resolveBackendConfig(cfg)
	if err != nil {
		return newConfigErrorBackend(err)
	}
	applyDowngradeDefaults(&resolved)
	rt.cfg = resolved
	rt.oauth = store
	if store == nil {
		if err := checkcfg.RequireNonEmpty(ID, "access_token", resolved.AccessToken); err != nil {
			return newConfigErrorBackend(err)
		}
	}
	return execbackend.Backend{
		Caps:            backendCaps,
		TransportCaps:   transportCaps(),
		BackendPrefixes: []string{ID},
		ModelInventory:  inventoryProvider(rt.cfg),
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return backendCaps
		},
		Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return rt.open(ctx, call, cand)
		},
	}
}

func resolveBackendConfig(cfg Config) (Config, *accountStore, error) {
	if !cfg.ManagedOAuthEnabled {
		resolved, err := resolveConfig(cfg)
		return resolved, nil, err
	}
	store, err := newAccountStore(cfg)
	if err != nil && !cfg.ManagedOAuthAllowAuthJSONFallback {
		return cfg, nil, err
	}
	if store != nil && store.hasUsable() {
		return cfg, store, nil
	}
	if !cfg.ManagedOAuthAllowAuthJSONFallback {
		if err != nil {
			return cfg, nil, err
		}
		return cfg, nil, fmt.Errorf("%s: no usable managed oauth accounts", ID)
	}
	resolved, err := resolveConfig(cfg)
	if err != nil {
		return cfg, nil, err
	}
	if strings.TrimSpace(resolved.AccessToken) == "" {
		return cfg, nil, fmt.Errorf("%s: no usable managed oauth accounts and auth json fallback has no access token", ID)
	}
	return resolved, nil, nil
}

func (rt *backendRuntime) open(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	rt.mu.Lock()
	cfg := rt.cfg
	store := rt.oauth
	rt.mu.Unlock()
	if store != nil {
		return openManaged(ctx, &cfg, store, call, cand)
	}
	return open(ctx, &cfg, rt, call, cand)
}

func openManaged(ctx context.Context, cfg *Config, store *accountStore, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand)
	if err != nil {
		return nil, err
	}
	for {
		acct, err := store.selectAccountForSession(env.convID)
		if err != nil {
			return nil, err
		}
		planType := firstNonEmpty(acct.PlanType, cfg.PlanTypeHint)
		policy := newDowngradePolicy(*cfg)
		body, err := env.marshalWithModel(policy.modelForPlan(env.originalModel, planType))
		if err != nil {
			return nil, err
		}
		callCfg := callCfgFromAccount(cfg, acct)
		attempt := env.newAttempt(ctx, cfg, call, body)
		resp, err := attempt.doRequest(&callCfg)
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			readLimitedClose(resp)
			store.markAuthInvalid(acct)
			continue
		case http.StatusTooManyRequests:
			readLimitedClose(resp)
			now := store.now()
			store.markRateLimited(acct, credpool.CooldownFromRetryAfterOrFallback(resp.Header.Get("Retry-After"), now, store.fallback))
			continue
		}
		es, finalResp, err := completeCodexOpenAttempt(attempt, resp, &callCfg)
		if err != nil {
			return nil, err
		}
		if qh := codexQuotaHeaders(finalResp.Header); len(qh) > 0 {
			_ = store.persistQuotaHeaders(acct, qh)
		}
		return es, nil
	}
}

func open(ctx context.Context, cfg *Config, rt *backendRuntime, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand)
	if err != nil {
		return nil, err
	}
	policy := newDowngradePolicy(*cfg)
	body, err := env.marshalWithModel(policy.modelForPlan(env.originalModel, cfg.PlanTypeHint))
	if err != nil {
		return nil, err
	}
	attempt := env.newAttempt(ctx, cfg, call, body)
	resp, err := attempt.doRequest(cfg)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		b := readLimitedClose(resp)
		if strings.TrimSpace(cfg.RefreshToken) == "" {
			return nil, upstreamHTTPError(resp.StatusCode, b)
		}
		refreshedCfg, refreshErr := refreshOAuthAccessToken(ctx, *cfg, env.client)
		if refreshErr != nil {
			return nil, fmt.Errorf("%s: oauth refresh after HTTP %d: %w", ID, resp.StatusCode, refreshErr)
		}
		*cfg = refreshedCfg
		rt.mu.Lock()
		rt.cfg.AccessToken = refreshedCfg.AccessToken
		if strings.TrimSpace(refreshedCfg.RefreshToken) != "" {
			rt.cfg.RefreshToken = refreshedCfg.RefreshToken
		}
		rt.mu.Unlock()
		resp, err = attempt.doRequest(cfg)
		if err != nil {
			return nil, err
		}
	}
	es, _, err := completeCodexOpenAttempt(attempt, resp, cfg)
	return es, err
}

func callCfgFromAccount(cfg *Config, acct managedAccount) Config {
	callCfg := *cfg
	callCfg.AccessToken = acct.AccessToken
	callCfg.AccountID = acct.ID
	if acct.RefreshToken != "" {
		callCfg.RefreshToken = acct.RefreshToken
	}
	return callCfg
}

func doCodexRequest(ctx context.Context, client *http.Client, endpoint string, body []byte, cfg *Config, convID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", ID, err)
	}
	applyCodexHeaders(req, *cfg, convID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", ID, err)
	}
	return resp, nil
}

func transportCaps() lipapi.BackendTransportCaps {
	return lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIResponses,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming, lipapi.TransportModeNonStreaming},
	})
}

func inventoryProvider(cfg Config) modelinventory.Provider {
	if len(cfg.Models) > 0 {
		models := make([]modelinventory.Model, 0, len(cfg.Models))
		for _, m := range cfg.Models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			models = append(models, modelinventory.Model{
				CanonicalID: ID + "/" + m,
				NativeID:    m,
				DisplayName: m,
			})
		}
		return modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: models,
		}
	}
	return modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticBuiltin,
		Models: builtinModels(),
	}
}

func builtinModels() []modelinventory.Model {
	out := make([]modelinventory.Model, 0, len(builtinCodexModelIDs))
	for _, id := range builtinCodexModelIDs {
		out = append(out, modelinventory.Model{
			CanonicalID: ID + "/" + id,
			NativeID:    id,
			DisplayName: id,
		})
	}
	return out
}

var builtinCodexModelIDs = []string{
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
	"gpt-5.2-codex",
	"gpt-5.2",
	"gpt-5.1-codex-max",
	"gpt-5.1-codex",
	"gpt-5.1-codex-mini",
	"gpt-5.1",
	"gpt-5-codex",
	"gpt-5-codex-mini",
	"gpt-5",
	"gpt-oss-120b",
	"gpt-oss-20b",
}

func newConfigErrorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            backendCaps,
		TransportCaps:   transportCaps(),
		BackendPrefixes: []string{ID},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) lipapi.BackendCaps {
			return backendCaps
		},
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
