package openaicodex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/checkcfg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/credpool"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// errManagedAccountsExhausted signals that the managed WebSocket open failed because
// every managed account was unusable due to account-level auth/rate-limit rejection,
// not a WebSocket transport problem. openWithFallback uses it to skip the global WS
// cooldown: the bad accounts are already marked in the credpool and excluded from
// future selection, so disabling WS for the whole backend would only delay recovery
// of accounts whose per-account cooldown expires sooner than the WS fallback window.
var errManagedAccountsExhausted = errors.New("managed oauth accounts exhausted")

var backendCaps = lipapi.NewBackendCaps(
	lipapi.CapabilityStreaming,
	lipapi.CapabilityTools,
	lipapi.CapabilityVision,
	lipapi.CapabilityDocuments,
	lipapi.CapabilityReasoning,
	lipapi.CapabilityParallelToolCalls,
)

type backendRuntime struct {
	mu           sync.Mutex
	cfg          Config
	oauth        *accountStore
	downgrade    downgradePolicy
	usageEst     *usageEstimator
	cooldown     *transportCooldown
	wsSessions   *wsSessionStore
	continuation *wsContinuationStore
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
	transport, err := NormalizeTransport(resolved.Transport, resolved.ExperimentalWebSocket)
	if err != nil {
		return newConfigErrorBackend(err)
	}
	resolved.Transport = transport
	if resolved.WebSocketFallbackCooldown <= 0 {
		resolved.WebSocketFallbackCooldown = DefaultWebSocketFallbackCooldown
	}
	usageEst, err := newUsageEstimator()
	if err != nil {
		return newConfigErrorBackend(err)
	}
	rt.downgrade = newDowngradePolicy(resolved)
	rt.cfg = resolved
	rt.oauth = store
	rt.usageEst = usageEst
	rt.cooldown = newTransportCooldown(resolved.WebSocketFallbackCooldown)
	rt.wsSessions = newWSSessionStore()
	rt.continuation = newWSContinuationStore(codexContinuationTTL, codexContinuationMaxEntries)
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
	usageEst := rt.usageEst
	cooldown := rt.cooldown
	wsSessions := rt.wsSessions
	continuation := rt.continuation
	downgrade := rt.downgrade
	rt.mu.Unlock()
	if store != nil {
		return openWithFallback(ctx, &cfg, cooldown,
			func() (lipapi.ManagedEventStream, error) {
				return openManaged(ctx, &cfg, store, call, cand, downgrade, usageEst)
			},
			func() (lipapi.ManagedEventStream, error) {
				return openManagedWS(ctx, &cfg, store, call, cand, downgrade, usageEst, wsSessions, continuation)
			},
		)
	}
	return openWithFallback(ctx, &cfg, cooldown,
		func() (lipapi.ManagedEventStream, error) { return openHTTP(ctx, &cfg, rt, downgrade, call, cand) },
		func() (lipapi.ManagedEventStream, error) {
			return openWS(ctx, &cfg, downgrade, usageEst, wsSessions, continuation, call, cand)
		},
	)
}

// openWithFallback orchestrates transport selection for both the static-token
// and managed paths. HTTPS is used directly when configured or when the WS
// cooldown is active; WebSocket is used strictly when configured; auto mode
// tries WS and falls back to HTTPS only on a WS fallback-eligible error,
// recording the cooldown. The openHTTPS/openWS closures carry the path-specific
// account wiring so this helper stays free of managed/static differences.
func openWithFallback(
	ctx context.Context,
	cfg *Config,
	cooldown *transportCooldown,
	openHTTPS, openWS func() (lipapi.ManagedEventStream, error),
) (lipapi.ManagedEventStream, error) {
	switch cfg.Transport {
	case TransportHTTPS:
		return openHTTPS()
	case TransportWebSocket:
		return openWS()
	default:
		if cooldown.active() {
			return openHTTPS()
		}
		es, err := openWS()
		if err == nil {
			return es, nil
		}
		// Account-level exhaustion from the managed WS path is not a WebSocket
		// transport problem: the bad accounts are already marked and excluded, and
		// HTTPS fallback may still succeed with a usable account. Skip the global WS
		// cooldown so a later-recovered account can use WS again without waiting out
		// the fallback window.
		if errors.Is(err, errManagedAccountsExhausted) {
			return openHTTPS()
		}
		if isWSFallbackError(ctx, err) {
			cooldown.markFailed()
			return openHTTPS()
		}
		return nil, err
	}
}

// selectManagedSession prepares the per-account session state shared by the WS
// and HTTP managed paths: picks an account for the conversation, derives the
// per-account call config, and resolves the plan-scoped model. The returned
// callCfg is a caller-owned copy so per-call mutation (e.g. OAuth refresh on
// the static path) never leaks back into the stored account config.
func selectManagedSession(env *codexOpenEnv, cfg *Config, store *accountStore, policy downgradePolicy) (managedAccount, Config, string, error) {
	acct, err := store.selectAccountForSession(env.convID)
	if err != nil {
		return managedAccount{}, Config{}, "", err
	}
	callCfg := callCfgFromAccount(cfg, acct)
	planType := firstNonEmpty(acct.PlanType, cfg.PlanTypeHint)
	return acct, callCfg, policy.modelForPlan(env.originalModel, planType), nil
}

type managedOpenAttemptFn func(ctx context.Context, env *codexOpenEnv, callCfg *Config, model string, usageEst *usageEstimator) (lipapi.ManagedEventStream, *http.Response, error)

func openManagedAccountLoop(ctx context.Context, cfg *Config, store *accountStore, call lipapi.Call, cand routing.AttemptCandidate, policy downgradePolicy, usageEst *usageEstimator, attempt managedOpenAttemptFn) (lipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand, policy)
	if err != nil {
		return nil, err
	}
	retries := maxManagedRetries(store)
	for range retries {
		acct, callCfg, model, err := selectManagedSession(env, cfg, store, policy)
		if err != nil {
			return nil, fmt.Errorf("%s: no usable managed oauth accounts: %w", ID, errManagedAccountsExhausted)
		}
		es, resp, err := attempt(ctx, env, &callCfg, model, usageEst)
		if err == nil {
			if resp != nil {
				if qh := codexQuotaHeaders(resp.Header); len(qh) > 0 {
					_ = store.persistQuotaHeaders(acct, qh)
				}
			}
			return es, nil
		}
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				store.markAuthInvalid(acct)
				continue
			case http.StatusTooManyRequests:
				now := store.now()
				store.markRateLimited(acct, credpool.CooldownFromRetryAfterOrFallback(resp.Header.Get("Retry-After"), now, store.fallback))
				continue
			}
		}
		return nil, err
	}
	return nil, fmt.Errorf("%s: no usable managed oauth accounts: %w", ID, errManagedAccountsExhausted)
}

func openManagedWS(ctx context.Context, cfg *Config, store *accountStore, call lipapi.Call, cand routing.AttemptCandidate, policy downgradePolicy, usageEst *usageEstimator, wsSessions *wsSessionStore, continuation *wsContinuationStore) (lipapi.ManagedEventStream, error) {
	return openManagedAccountLoop(ctx, cfg, store, call, cand, policy, usageEst, func(ctx context.Context, env *codexOpenEnv, callCfg *Config, model string, usageEst *usageEstimator) (lipapi.ManagedEventStream, *http.Response, error) {
		return openWSPrepared(ctx, env, callCfg, model, call, usageEst, wsSessions, continuation)
	})
}

func openManaged(ctx context.Context, cfg *Config, store *accountStore, call lipapi.Call, cand routing.AttemptCandidate, policy downgradePolicy, usageEst *usageEstimator) (lipapi.ManagedEventStream, error) {
	return openManagedAccountLoop(ctx, cfg, store, call, cand, policy, usageEst, func(ctx context.Context, env *codexOpenEnv, callCfg *Config, model string, usageEst *usageEstimator) (lipapi.ManagedEventStream, *http.Response, error) {
		body, err := env.marshalWithModel(model)
		if err != nil {
			return nil, nil, err
		}
		attempt := env.newAttempt(ctx, cfg, call, body, usageEst)
		resp, err := attempt.doRequest(callCfg)
		if err != nil {
			return nil, nil, err
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			b := readLimitedClose(resp)
			return nil, resp, upstreamHTTPError(resp.StatusCode, b)
		}
		return completeCodexOpenAttempt(attempt, resp, callCfg)
	})
}

func maxManagedRetries(store *accountStore) int {
	if store == nil || len(store.meta) == 0 {
		return 1
	}
	return len(store.meta)
}

func openHTTP(ctx context.Context, cfg *Config, rt *backendRuntime, policy downgradePolicy, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand, policy)
	if err != nil {
		return nil, err
	}
	body, err := env.marshalWithModel(policy.modelForPlan(env.originalModel, cfg.PlanTypeHint))
	if err != nil {
		return nil, err
	}
	attempt := env.newAttempt(ctx, cfg, call, body, rt.usageEst)
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
	start := time.Now()
	if debugTurnsEnabled() {
		slog.DebugContext(ctx, "openaicodex.debug.http_request_start",
			"endpoint", endpoint,
			"body_bytes", len(body),
			"conversation_id", convID,
		)
	}
	resp, err := client.Do(req)
	if err != nil {
		if debugTurnsEnabled() {
			slog.DebugContext(ctx, "openaicodex.debug.http_request_done",
				"endpoint", endpoint,
				"duration_ms", time.Since(start).Milliseconds(),
				"status", "error",
				"error", err.Error(),
			)
		}
		return nil, fmt.Errorf("%s: request: %w", ID, err)
	}
	if debugTurnsEnabled() {
		slog.DebugContext(ctx, "openaicodex.debug.http_request_done",
			"endpoint", endpoint,
			"duration_ms", time.Since(start).Milliseconds(),
			"status", resp.StatusCode,
		)
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
	"gpt-5.3-codex-spark",
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
