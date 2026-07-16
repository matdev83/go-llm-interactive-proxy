package openaicodex_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func latestCodexVerbosity(t *testing.T, srv *refbackend.Server) string {
	t.Helper()
	body := srv.LatestRequest().Body
	if body == nil {
		return ""
	}
	text, _ := body["text"].(map[string]any)
	if text == nil {
		return ""
	}
	v, _ := text["verbosity"].(string)
	return v
}

// TestOpen_autoFallsBackWhenWSStopsAfterLifecycleEvent verifies that a bare
// response.created lifecycle event does not commit the WebSocket attempt. Auto
// mode may still fall back to HTTPS when WS closes before user-visible output.
func TestOpen_autoFallsBackWhenWSStopsAfterLifecycleEvent(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailureAfterFirstEvent,
	})
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
		t.Fatalf("open: %v", err)
	}
	events := drainEvents(t, es)

	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https (fallback before committed output)", got)
	}
	if !slices.Contains(eventKindsList(events), lipapi.EventTextDelta) {
		t.Fatalf("missing HTTPS fallback text delta: %+v", events)
	}
}

// TestOpen_autoFallbackCountsOnlySuccessfulTurn verifies that a WS pre-first-event
// fallback to HTTPS still consumes only one successful turn in the verbosity
// counters, so the next request remains inside the early-session window.
func TestOpen_autoFallbackCountsOnlySuccessfulTurn(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailurePolicyCloseBeforeEvent,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:                          ts.URL + "/backend-api/codex",
		AccessToken:                      "sk-codex",
		HTTPClient:                       ts.Client(),
		Transport:                        backend.TransportAuto,
		ExperimentalWebSocket:            true,
		EarlySessionVerbosityBumpTurns:   2,
		MidSessionVerbosityBumpFrequency: 10,
	})
	call := codexCall()
	call.Session.ContinuityKey = "conv-auto-fallback-turns"

	first, err := be.Open(context.Background(), call, codexCand())
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	drainEvents(t, first)
	if got := latestCodexVerbosity(t, srv); got != "high" {
		t.Fatalf("first open verbosity = %q, want high", got)
	}

	second, err := be.Open(context.Background(), call, codexCand())
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	drainEvents(t, second)
	if got := latestCodexVerbosity(t, srv); got != "high" {
		t.Fatalf("second open verbosity = %q, want high after one successful turn", got)
	}
}

// TestOpen_websocketModeSuccess exercises the strict websocket transport on the
// happy path: the call completes over WS and never touches HTTPS.
func TestOpen_websocketModeSuccess(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ws-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             backend.TransportWebSocket,
		ExperimentalWebSocket: true,
	})
	es, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, es)
	if got := srv.LatestRequest().Transport; got != "websocket" {
		t.Fatalf("transport = %q, want websocket", got)
	}
	if !slices.Contains(eventKindsList(events), lipapi.EventTextDelta) {
		t.Fatalf("missing text delta: %+v", events)
	}
}

// TestOpen_cooldownExpiryRetriesWS verifies that after the fallback cooldown
// window elapses, auto mode retries WebSocket (and succeeds) instead of staying
// on HTTPS permanently.
func TestOpen_cooldownExpiryRetriesWS(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "ws-ok-after",
		ForcedWSFailure: refbackend.WSFailurePolicyCloseBeforeEvent,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:                   ts.URL + "/backend-api/codex",
		AccessToken:               "sk-codex",
		HTTPClient:                ts.Client(),
		Transport:                 backend.TransportAuto,
		ExperimentalWebSocket:     true,
		WebSocketFallbackCooldown: 50 * time.Millisecond,
	})

	first, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, first)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("first transport = %q, want https (fallback after WS fail)", got)
	}

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		second, err := be.Open(context.Background(), codexCall(), codexCand())
		if err != nil {
			t.Fatal(err)
		}
		drainEvents(t, second)
		if got := srv.LatestRequest().Transport; got == "websocket" {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("second transport = %q, want websocket (cooldown expired, auto must retry WS)", srv.LatestRequest().Transport)
		case <-ticker.C:
		}
	}
}

func TestOpen_autoFallsBackOnWSNormalCloseBeforeFirstEvent(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailureNormalCloseBeforeEvent,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:                   ts.URL + "/backend-api/codex",
		AccessToken:               "sk-codex",
		HTTPClient:                ts.Client(),
		Transport:                 backend.TransportAuto,
		ExperimentalWebSocket:     true,
		WebSocketFallbackCooldown: time.Hour,
	})

	first, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, first)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("first transport = %q, want https (normal close before first event)", got)
	}

	second, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, second)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("second transport = %q, want https (cooldown after normal close fallback)", got)
	}
}

// TestOpen_failedHTTPOpenDoesNotAdvanceVerbosityTurn verifies that an Open error
// does not consume a verbosity-bump slot, so a subsequent successful request for
// the same conversation still sees turn 1.
func TestOpen_failedHTTPOpenDoesNotAdvanceVerbosityTurn(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:            "sk-codex",
		OutputText:       "http-ok",
		ForcedHTTPStatus: http.StatusTooManyRequests,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:                        ts.URL + "/backend-api/codex",
		AccessToken:                    "sk-codex",
		HTTPClient:                     ts.Client(),
		Transport:                      backend.TransportHTTPS,
		EarlySessionVerbosityBumpTurns: 1,
	})
	call := codexCall()
	call.Session.ContinuityKey = "conv-failed-open"

	if _, err := be.Open(context.Background(), call, codexCand()); err == nil {
		t.Fatal("expected first open to fail")
	}

	es, err := be.Open(context.Background(), call, codexCand())
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	drainEvents(t, es)
	if got := latestCodexVerbosity(t, srv); got != "high" {
		t.Fatalf("second open verbosity = %q, want high after failed first open", got)
	}
}

func TestOpen_autoFallsBackOnWSNoCanonicalFirstFrameThenClose(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailureNoCanonicalFirstFrame,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:                   ts.URL + "/backend-api/codex",
		AccessToken:               "sk-codex",
		HTTPClient:                ts.Client(),
		Transport:                 backend.TransportAuto,
		ExperimentalWebSocket:     true,
		WebSocketFallbackCooldown: time.Hour,
	})

	first, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, first)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("first transport = %q, want https (no canonical first frame then close)", got)
	}

	second, err := be.Open(context.Background(), codexCall(), codexCand())
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, second)
	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("second transport = %q, want https (cooldown after no-canonical fallback)", got)
	}
}

func TestOpen_autoNoFallbackOnWSMalformedFirstFrame(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailureMalformedFirstFrame,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := backend.New(backend.Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             backend.TransportAuto,
		ExperimentalWebSocket: true,
	})
	_, err := be.Open(context.Background(), codexCall(), codexCand())
	if err == nil {
		t.Fatal("expected open failure without HTTPS fallback")
	}
	if got := srv.LatestRequest().Transport; got == "https" {
		t.Fatalf("transport = %q, must not fall back to HTTPS on mapper error", got)
	}
}
