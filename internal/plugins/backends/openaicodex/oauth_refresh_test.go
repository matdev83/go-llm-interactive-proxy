package openaicodex_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
)

func TestOpen_oauthRefreshRetriesOnceOn401(t *testing.T) {
	t.Parallel()

	var refreshCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		refreshCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "new-token",
			"refresh_token": "new-refresh",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	srv := refbackend.New(refbackend.Config{Token: "new-token", OutputText: "refreshed-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:       ts.URL + "/backend-api/codex",
		AccessToken:   "old-token",
		RefreshToken:  "refresh-me",
		OAuthTokenURL: tokenSrv.URL,
		HTTPClient:    ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Authorization; got != "Bearer new-token" {
		t.Fatalf("authorization after refresh: %q", got)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls: %d", refreshCalls.Load())
	}
}

func TestOpen_oauthRefreshFailureReturnsError(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Cleanup(tokenSrv.Close)

	srv := refbackend.New(refbackend.Config{Token: "expected-token"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:       ts.URL + "/backend-api/codex",
		AccessToken:   "old-token",
		RefreshToken:  "bad-refresh",
		OAuthTokenURL: tokenSrv.URL,
		HTTPClient:    ts.Client(),
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected refresh failure error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") && !strings.Contains(msg, "403") {
		t.Fatalf("expected auth status in error: %v", err)
	}
	if !strings.Contains(msg, "refresh") {
		t.Fatalf("expected refresh context in error: %v", err)
	}
	if strings.Contains(msg, "bad-refresh") || strings.Contains(msg, "old-token") {
		t.Fatalf("error must not leak secrets: %v", err)
	}
}

func TestOpen_401WithoutRefreshTokenDoesNotRetry(t *testing.T) {
	t.Parallel()

	var refreshCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(tokenSrv.Close)

	srv := refbackend.New(refbackend.Config{Token: "expected-token"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:       ts.URL + "/backend-api/codex",
		AccessToken:   "wrong-token",
		OAuthTokenURL: tokenSrv.URL,
		HTTPClient:    ts.Client(),
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected auth error")
	}
	if refreshCalls.Load() != 0 {
		t.Fatalf("refresh calls: %d", refreshCalls.Load())
	}
}

func TestOpen_oauthRefreshErrorTruncatesLongBody(t *testing.T) {
	t.Parallel()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, strings.Repeat("z", 5000))
	}))
	t.Cleanup(tokenSrv.Close)

	srv := refbackend.New(refbackend.Config{Token: "expected-token"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:       ts.URL + "/backend-api/codex",
		AccessToken:   "old-token",
		RefreshToken:  "bad-refresh",
		OAuthTokenURL: tokenSrv.URL,
		HTTPClient:    ts.Client(),
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected refresh failure error")
	}
	msg := err.Error()
	if strings.Contains(msg, strings.Repeat("z", 300)) {
		t.Fatalf("error leaks long token-endpoint body (len=%d)", len(msg))
	}
	if !strings.Contains(msg, "refresh") {
		t.Fatalf("expected refresh context in error: %q", msg)
	}
}
