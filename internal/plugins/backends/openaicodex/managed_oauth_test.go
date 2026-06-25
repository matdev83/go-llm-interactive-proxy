package openaicodex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func writeAccountFile(t *testing.T, dir, name string, fields map[string]any) {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func managedOAuthCfg(dir string) backend.Config {
	return backend.Config{
		BaseURL:                           "http://127.0.0.1",
		ManagedOAuthEnabled:               true,
		ManagedOAuthStoragePath:           dir,
		ManagedOAuthSelectionStrategy:     "first-available",
		ManagedOAuthAllowAuthJSONFallback: false,
		RateLimitFallback:                 60 * time.Second,
	}
}

func TestManagedOAuth_loadsAccountFilesAndUsesTokenAndAccountHeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "acct1.json", map[string]any{
		"account_id":   "acct-one",
		"access_token": "tok-one",
	})

	srv := refbackend.New(refbackend.Config{Token: "tok-one", OutputText: "managed-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = ts.URL + "/backend-api/codex"
	cfg.HTTPClient = ts.Client()
	be := backend.New(cfg)
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	got := srv.LatestRequest()
	if got.Authorization != "Bearer tok-one" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.ChatGPTAccountID != "acct-one" {
		t.Fatalf("account id: %q", got.ChatGPTAccountID)
	}
}

func TestManagedOAuth_roundRobinCyclesTwoAccounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "a.json", map[string]any{
		"account_id":   "acct-a",
		"access_token": "tok-a",
	})
	writeAccountFile(t, dir, "b.json", map[string]any{
		"account_id":   "acct-b",
		"access_token": "tok-b",
	})

	var lastAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		switch auth {
		case "Bearer tok-a", "Bearer tok-b":
			lastAuth.Store(auth)
		default:
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		refbackend.New(refbackend.Config{Token: strings.TrimPrefix(auth, "Bearer ")}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = srv.URL + "/backend-api/codex"
	cfg.HTTPClient = srv.Client()
	cfg.ManagedOAuthSelectionStrategy = "round-robin"
	be := backend.New(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}

	es1, err := be.Open(context.Background(), sampleCall(), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es1)
	first, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}

	es2, err := be.Open(context.Background(), sampleCall(), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es2)
	second, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}

	if first == second {
		t.Fatalf("expected different accounts, both %q", first)
	}
	if (first == "Bearer tok-a" && second != "Bearer tok-b") || (first == "Bearer tok-b" && second != "Bearer tok-a") {
		t.Fatalf("round-robin pair: first=%q second=%q", first, second)
	}
}

func TestManagedOAuth_401OnFirstAccountRetriesSecondAndMarksFirstInvalid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "bad.json", map[string]any{
		"account_id":   "acct-bad",
		"access_token": "tok-bad",
	})
	writeAccountFile(t, dir, "good.json", map[string]any{
		"account_id":   "acct-good",
		"access_token": "tok-good",
	})

	var lastAuth atomic.Value
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		auth := r.Header.Get("Authorization")
		lastAuth.Store(auth)
		if auth == "Bearer tok-bad" {
			http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
			return
		}
		refbackend.New(refbackend.Config{Token: "tok-good"}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = srv.URL + "/backend-api/codex"
	cfg.HTTPClient = srv.Client()
	be := backend.New(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}

	es, err := be.Open(context.Background(), sampleCall(), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if calls.Load() < 2 {
		t.Fatalf("expected retry on first open, calls=%d", calls.Load())
	}

	before := calls.Load()
	es2, err := be.Open(context.Background(), sampleCall(), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es2)
	got, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}
	if got != "Bearer tok-good" {
		t.Fatalf("second open auth: %q", got)
	}
	if calls.Load()-before != 1 {
		t.Fatalf("second open should not retry bad account, calls=%d", calls.Load()-before)
	}
}

func TestManagedOAuth_429WithRetryAfterRetriesSecondAndCooldownExcludesFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "limited.json", map[string]any{
		"account_id":   "acct-lim",
		"access_token": "tok-lim",
	})
	writeAccountFile(t, dir, "spare.json", map[string]any{
		"account_id":   "acct-spare",
		"access_token": "tok-spare",
	})

	var lastAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		lastAuth.Store(auth)
		if auth == "Bearer tok-lim" {
			w.Header().Set("Retry-After", "3600")
			http.Error(w, `{"error":"rate_limit"}`, http.StatusTooManyRequests)
			return
		}
		refbackend.New(refbackend.Config{Token: "tok-spare"}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = srv.URL + "/backend-api/codex"
	cfg.HTTPClient = srv.Client()
	be := backend.New(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}

	es, err := be.Open(context.Background(), sampleCall(), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	got, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}
	if got != "Bearer tok-spare" {
		t.Fatalf("first open should land on spare: %q", got)
	}

	es2, err := be.Open(context.Background(), sampleCall(), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es2)
	got, ok = lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}
	if got != "Bearer tok-spare" {
		t.Fatalf("cooldown should skip limited account: %q", got)
	}
}

func TestManagedOAuth_noUsableAccountsAllowFallbackFalseErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "broken.json", map[string]any{"email": "x@y.z"})

	cfg := managedOAuthCfg(dir)
	be := backend.New(cfg)
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "managed") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagedOAuth_allowFallbackTrueUsesAuthJSONPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"fallback-tok","account_id":"fallback-acct"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := refbackend.New(refbackend.Config{Token: "fallback-tok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = ts.URL + "/backend-api/codex"
	cfg.HTTPClient = ts.Client()
	cfg.ManagedOAuthAllowAuthJSONFallback = true
	cfg.AuthJSONPath = authPath
	be := backend.New(cfg)
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	got := srv.LatestRequest()
	if got.Authorization != "Bearer fallback-tok" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
}

func callWithSession(sessionID string) lipapi.Call {
	call := sampleCall()
	call.Session.ClientSessionID = sessionID
	return call
}

func TestManagedOAuth_sessionAffinityReusesAccountAcrossCalls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "a.json", map[string]any{
		"account_id":   "acct-a",
		"access_token": "tok-a",
	})
	writeAccountFile(t, dir, "b.json", map[string]any{
		"account_id":   "acct-b",
		"access_token": "tok-b",
	})

	var lastAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		switch auth {
		case "Bearer tok-a", "Bearer tok-b":
			lastAuth.Store(auth)
		default:
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		refbackend.New(refbackend.Config{Token: strings.TrimPrefix(auth, "Bearer ")}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = srv.URL + "/backend-api/codex"
	cfg.HTTPClient = srv.Client()
	cfg.ManagedOAuthSelectionStrategy = "session-affinity"
	be := backend.New(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}

	es1, err := be.Open(context.Background(), callWithSession("sess-sticky"), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es1)
	first, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}

	es2, err := be.Open(context.Background(), callWithSession("sess-sticky"), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es2)
	second, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}

	if first != second {
		t.Fatalf("same session should reuse account: first=%q second=%q", first, second)
	}
}

func TestManagedOAuth_sessionAffinityDifferentSessions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "a.json", map[string]any{
		"account_id":   "acct-a",
		"access_token": "tok-a",
	})
	writeAccountFile(t, dir, "b.json", map[string]any{
		"account_id":   "acct-b",
		"access_token": "tok-b",
	})

	var lastAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		lastAuth.Store(auth)
		refbackend.New(refbackend.Config{Token: strings.TrimPrefix(auth, "Bearer ")}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = srv.URL + "/backend-api/codex"
	cfg.HTTPClient = srv.Client()
	cfg.ManagedOAuthSelectionStrategy = "session-affinity"
	be := backend.New(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}

	es1, err := be.Open(context.Background(), callWithSession("sess-a"), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es1)
	first, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}

	es2, err := be.Open(context.Background(), callWithSession("sess-b"), cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es2)
	second, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}

	if first == second {
		t.Fatalf("different sessions should get different accounts: both %q", first)
	}
}

func TestManagedOAuth_sessionAffinity401RotatesForSameSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "bad.json", map[string]any{
		"account_id":   "acct-bad",
		"access_token": "tok-bad",
	})
	writeAccountFile(t, dir, "good.json", map[string]any{
		"account_id":   "acct-good",
		"access_token": "tok-good",
	})

	var lastAuth atomic.Value
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		auth := r.Header.Get("Authorization")
		lastAuth.Store(auth)
		if auth == "Bearer tok-bad" {
			http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
			return
		}
		refbackend.New(refbackend.Config{Token: "tok-good"}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = srv.URL + "/backend-api/codex"
	cfg.HTTPClient = srv.Client()
	cfg.ManagedOAuthSelectionStrategy = "session-affinity"
	be := backend.New(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex"}}
	call := callWithSession("sess-retry")

	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if calls.Load() < 2 {
		t.Fatalf("expected retry, calls=%d", calls.Load())
	}

	before := calls.Load()
	es2, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es2)
	got, ok := lastAuth.Load().(string)
	if !ok {
		t.Fatal("lastAuth not string")
	}
	if got != "Bearer tok-good" {
		t.Fatalf("second open auth: %q", got)
	}
	if calls.Load()-before != 1 {
		t.Fatalf("second open should not retry bad account, calls=%d", calls.Load()-before)
	}
}

func TestManagedOAuth_quotaHeadersPersistedOnSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	acctPath := filepath.Join(dir, "acct.json")
	if err := os.WriteFile(acctPath, []byte(`{"account_id":"acct-q","access_token":"tok-q","email":"q@test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := refbackend.New(refbackend.Config{
		Token:        "tok-q",
		PlanType:     "pro",
		UsagePercent: "77",
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = ts.URL + "/backend-api/codex"
	cfg.HTTPClient = ts.Client()
	be := backend.New(cfg)
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	b, err := os.ReadFile(acctPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	qh, ok := saved["quota_headers"]
	if !ok {
		t.Fatalf("missing quota_headers: %s", b)
	}
	var headers map[string]string
	if err := json.Unmarshal(qh, &headers); err != nil {
		t.Fatal(err)
	}
	if headers["x-codex-plan-type"] != "pro" {
		t.Fatalf("plan type: %q", headers["x-codex-plan-type"])
	}
	if headers["x-codex-primary-used-percent"] != "77" {
		t.Fatalf("usage percent: %q", headers["x-codex-primary-used-percent"])
	}
	if _, ok := saved["email"]; !ok {
		t.Fatal("email field lost")
	}
	if _, ok := saved["access_token"]; !ok {
		t.Fatal("access_token field lost")
	}
}
