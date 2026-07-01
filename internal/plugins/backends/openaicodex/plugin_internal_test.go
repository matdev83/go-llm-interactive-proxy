package openaicodex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOpenWithFallback_managedExhaustionSkipsGlobalCooldown(t *testing.T) {
	t.Parallel()
	now := time.Time{}
	cooldown := &transportCooldown{cooldown: time.Hour, now: func() time.Time { return now }}
	cfg := &Config{Transport: TransportAuto}
	var httpsCalls int
	es, err := openWithFallback(context.Background(), cfg, cooldown,
		func() (lipapi.ManagedEventStream, error) {
			httpsCalls++
			return nil, errors.New("https failed")
		},
		func() (lipapi.ManagedEventStream, error) {
			return nil, fmt.Errorf("ws accounts exhausted: %w", errManagedAccountsExhausted)
		},
	)
	if err == nil || es != nil {
		t.Fatalf("expected https fallback error, got es=%v err=%v", es, err)
	}
	if httpsCalls != 1 {
		t.Fatalf("httpsCalls = %d, want 1", httpsCalls)
	}
	if cooldown.active() {
		t.Fatal("managed account exhaustion must not activate global WS cooldown")
	}
}

func TestOpenManagedAccountLoop_httpExhaustionClassifiesManagedAccountsExhausted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.json", "b.json"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"account_id":"`+name+`","access_token":"tok-`+name+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := newAccountStore(Config{
		ManagedOAuthEnabled:           true,
		ManagedOAuthStoragePath:       dir,
		ManagedOAuthSelectionStrategy: "first-available",
		RateLimitFallback:             time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{BaseURL: "http://127.0.0.1/backend-api/codex/responses"}
	_, err = openManagedAccountLoop(context.Background(), &cfg, store, lipapi.Call{}, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	}, newDowngradePolicy(cfg), nil, func(_ context.Context, _ *codexOpenEnv, _ *Config, _ string, _ *usageEstimator) (lipapi.ManagedEventStream, *http.Response, error) {
		return nil, &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}, fmt.Errorf("account rate limited")
	})
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if !errors.Is(err, errManagedAccountsExhausted) {
		t.Fatalf("err = %v, want errManagedAccountsExhausted", err)
	}
}

func TestOpenWithFallback_transportFailureActivatesCooldown(t *testing.T) {
	t.Parallel()
	now := time.Time{}
	cooldown := &transportCooldown{cooldown: time.Hour, now: func() time.Time { return now }}
	cfg := &Config{Transport: TransportAuto}
	_, _ = openWithFallback(context.Background(), cfg, cooldown,
		func() (lipapi.ManagedEventStream, error) { return nil, errors.New("https ok path") },
		func() (lipapi.ManagedEventStream, error) {
			return nil, newWSTransportError(errors.New("dial failed"))
		},
	)
	if !cooldown.active() {
		t.Fatal("transport WS failure must activate global WS cooldown")
	}
}

func TestOpenWithFallback_nonTransportFailureSkipsCooldown(t *testing.T) {
	t.Parallel()
	now := time.Time{}
	cooldown := &transportCooldown{cooldown: time.Hour, now: func() time.Time { return now }}
	cfg := &Config{Transport: TransportAuto}
	_, err := openWithFallback(context.Background(), cfg, cooldown,
		func() (lipapi.ManagedEventStream, error) { return nil, errors.New("https must not run") },
		func() (lipapi.ManagedEventStream, error) {
			return nil, fmt.Errorf("%s: marshal payload: %w", ID, errors.New("bad field"))
		},
	)
	if err == nil {
		t.Fatal("expected non-transport error to propagate")
	}
	if cooldown.active() {
		t.Fatal("programmer/data errors must not activate global WS cooldown")
	}
}
