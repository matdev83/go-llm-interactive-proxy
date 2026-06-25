package openaicodex_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestOpen_payloadIncludesPromptCacheKeyFromSession(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	call := lipapi.Call{
		ID: "call-fallback",
		Session: lipapi.SessionRef{
			ClientSessionID: "sess-cache-key",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	got := srv.LatestRequest().Body["prompt_cache_key"]
	if got != "sess-cache-key" {
		t.Fatalf("prompt_cache_key: %#v", got)
	}
}

func TestOpen_payloadPromptCacheKeyFallsBackToCallID(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	call := lipapi.Call{
		ID: "call-only-id",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	got := srv.LatestRequest().Body["prompt_cache_key"]
	if got != "call-only-id" {
		t.Fatalf("prompt_cache_key: %#v", got)
	}
}
