package openaicodex_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func codexCall() lipapi.Call {
	return lipapi.Call{
		ID: "ws-call",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func codexCand() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}
}

func eventKindsList(events []lipapi.Event) []lipapi.EventKind {
	out := make([]lipapi.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func TestOpen_autoTransportUsesWebSocket(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ws-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             backend.TransportAuto,
		ExperimentalWebSocket: true,
	})
	es, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, es)
	if err := lipapi.ValidateEventSequence(events); err != nil {
		t.Fatal(err)
	}
	if got := srv.LatestRequest().Transport; got != "websocket" {
		t.Fatalf("transport = %q, want websocket (auto must try WS first)", got)
	}
	if !slices.Contains(eventKindsList(events), lipapi.EventTextDelta) {
		t.Fatalf("missing text delta: %+v", events)
	}
}

func TestOpen_defaultTransportUsesHTTPS(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "https-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
	})
	es, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https (websocket is experimental opt-in)", got)
	}
}

func TestOpen_autoFallsBackToHTTPOnWSFail(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "http-ok", ForcedWSFailure: refbackend.WSFailurePolicyCloseBeforeEvent})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             backend.TransportAuto,
		ExperimentalWebSocket: true,
	})
	es, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https (auto must fall back after WS fail-before-first-event)", got)
	}
}

func TestOpen_websocketModeNoFallback(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok", ForcedWSFailure: refbackend.WSFailurePolicyCloseBeforeEvent})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             backend.TransportWebSocket,
		ExperimentalWebSocket: true,
	})
	_, err := be.Open(context.Background(), codexCall(), codexCand())
	if err == nil {
		t.Fatal("expected websocket-only mode to surface WS failure without fallback")
	}
	if got := srv.LatestRequest().Transport; got == "https" {
		t.Fatalf("websocket mode must not fall back to HTTPS; transport=%q", got)
	}
}

func TestOpen_httpsModeNeverUsesWebSocket(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
		Transport:   backend.TransportHTTPS,
	})
	es, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https (https mode must never dial WS)", got)
	}
}

func TestOpen_cooldownSkipsWSAfterFailure(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ok", ForcedWSFailure: refbackend.WSFailurePolicyCloseBeforeEvent})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             backend.TransportAuto,
		ExperimentalWebSocket: true,
	})
	first, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, first)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("first call transport = %q, want https (fallback)", got)
	}

	// Second call: WS would now succeed (one-shot fail consumed), but cooldown
	// must keep auto mode on HTTPS.
	second, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, second)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("second call transport = %q, want https (cooldown must skip WS)", got)
	}
}

func TestOpen_invalidTransportConfigError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(refbackend.New(refbackend.Config{Token: "sk-codex"}).Handler())
	t.Cleanup(ts.Close)
	be := backend.New(backend.Config{
		BaseURL:     ts.URL + "/backend-api/codex",
		AccessToken: "sk-codex",
		HTTPClient:  ts.Client(),
		Transport:   "quic",
	})
	_, err := be.Open(context.Background(), codexCall(), codexCand())
	if err == nil {
		t.Fatal("expected config error for invalid transport")
	}
	_, err = be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected inventory config error for invalid transport")
	}
}
