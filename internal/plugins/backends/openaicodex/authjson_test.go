package openaicodex_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
)

func writeAuthJSON(t *testing.T, dir string, v any) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withHomeDir(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func writeDefaultCodexAuthJSON(t *testing.T, home string, v any) {
	t.Helper()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAuthJSON(t, codexDir, v)
}

func TestOpen_authJSONPathLoadsToken(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authPath := writeAuthJSON(t, dir, map[string]any{
		"tokens": map[string]any{
			"access_token": "from-auth-json",
		},
		"account_id": "acct-from-json",
	})

	srv := refbackend.New(refbackend.Config{Token: "from-auth-json", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:      ts.URL + "/backend-api/codex",
		AuthJSONPath: authPath,
		HTTPClient:   ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	got := srv.LatestRequest()
	if got.Authorization != "Bearer from-auth-json" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.ChatGPTAccountID != "acct-from-json" {
		t.Fatalf("account id: %q", got.ChatGPTAccountID)
	}
}

func TestOpen_explicitAccessTokenOverridesAuthJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authPath := writeAuthJSON(t, dir, map[string]any{
		"tokens": map[string]any{"access_token": "json-token"},
	})

	srv := refbackend.New(refbackend.Config{Token: "explicit-token", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:      ts.URL + "/backend-api/codex",
		AccessToken:  "explicit-token",
		AuthJSONPath: authPath,
		HTTPClient:   ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Authorization; got != "Bearer explicit-token" {
		t.Fatalf("authorization: %q", got)
	}
}

func TestNew_missingAccessTokenInAuthJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authPath := writeAuthJSON(t, dir, map[string]any{
		"tokens": map[string]any{"refresh_token": "only-refresh"},
	})

	be := backend.New(backend.Config{
		BaseURL:      "http://127.0.0.1",
		AuthJSONPath: authPath,
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected config error")
	}
}

func TestOpen_authJSONCamelCaseFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authPath := writeAuthJSON(t, dir, map[string]any{
		"accessToken":  "camel-token",
		"accountID":    "camel-acct",
		"refreshToken": "camel-refresh",
	})

	srv := refbackend.New(refbackend.Config{Token: "camel-token", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:      ts.URL + "/backend-api/codex",
		AuthJSONPath: authPath,
		HTTPClient:   ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	got := srv.LatestRequest()
	if got.Authorization != "Bearer camel-token" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.ChatGPTAccountID != "camel-acct" {
		t.Fatalf("account id: %q", got.ChatGPTAccountID)
	}
}

func TestOpen_defaultAuthJSONPathLoadsToken(t *testing.T) { //nolint:paralleltest // mutates HOME/USERPROFILE via t.Setenv
	home := t.TempDir()
	withHomeDir(t, home)
	writeDefaultCodexAuthJSON(t, home, map[string]any{
		"tokens": map[string]any{"access_token": "default-path-token"},
	})

	srv := refbackend.New(refbackend.Config{Token: "default-path-token", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:    ts.URL + "/backend-api/codex",
		HTTPClient: ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Authorization; got != "Bearer default-path-token" {
		t.Fatalf("authorization: %q", got)
	}
}

func TestOpen_explicitAuthJSONPathOverridesDefaultDiscovery(t *testing.T) { //nolint:paralleltest // mutates HOME/USERPROFILE via t.Setenv
	home := t.TempDir()
	withHomeDir(t, home)
	writeDefaultCodexAuthJSON(t, home, map[string]any{
		"tokens": map[string]any{"access_token": "default-token"},
	})
	explicitDir := t.TempDir()
	explicitPath := writeAuthJSON(t, explicitDir, map[string]any{
		"tokens": map[string]any{"access_token": "explicit-token"},
	})

	srv := refbackend.New(refbackend.Config{Token: "explicit-token", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:      ts.URL + "/backend-api/codex",
		AuthJSONPath: explicitPath,
		HTTPClient:   ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Authorization; got != "Bearer explicit-token" {
		t.Fatalf("authorization: %q", got)
	}
}

func TestNew_missingDefaultAuthJSONKeepsAccessTokenError(t *testing.T) { //nolint:paralleltest // mutates HOME/USERPROFILE via t.Setenv
	withHomeDir(t, t.TempDir())

	be := backend.New(backend.Config{
		BaseURL: "http://127.0.0.1",
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected config error")
	}
	if !strings.Contains(err.Error(), "access_token") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpen_explicitAccountIDOverridesAuthJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authPath := writeAuthJSON(t, dir, map[string]any{
		"accessToken": "tok",
		"accountID":   "json-acct",
	})

	srv := refbackend.New(refbackend.Config{Token: "tok", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:      ts.URL + "/backend-api/codex",
		AccountID:    "explicit-acct",
		AuthJSONPath: authPath,
		HTTPClient:   ts.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().ChatGPTAccountID; got != "explicit-acct" {
		t.Fatalf("account id: %q", got)
	}
}
