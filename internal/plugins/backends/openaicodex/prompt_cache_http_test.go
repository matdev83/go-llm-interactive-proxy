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
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
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

func TestOpen_payloadUsesAuthoritativeSessionBeforeClientHint(t *testing.T) {
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
			ClientSessionID:        "client-controlled-session",
			AuthoritativeSessionID: "proxy-authoritative-session",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)

	got := srv.LatestRequest().Body["prompt_cache_key"]
	if got != "proxy-authoritative-session" {
		t.Fatalf("prompt_cache_key: %#v", got)
	}
	if got := srv.LatestRequest().ConversationID; got != "proxy-authoritative-session" {
		t.Fatalf("conversation_id: %q", got)
	}
	if got := srv.LatestRequest().SessionID; got != "proxy-authoritative-session" {
		t.Fatalf("session_id: %q", got)
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
		Primary: routing.Primary{Model: "gpt-5.3-codex-spark"},
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
