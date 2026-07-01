package openaicodex

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func internalCodexCall() lipapi.Call {
	return lipapi.Call{
		ID: "ws-stall-call",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
}

func internalCodexCand() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5.3-codex-spark"}}
}

// drainUntilEnd reads events until the stream terminates, accepting either EOF
// or a stream error as a valid end. Used where a mid-stream failure is expected.
func drainUntilEnd(t *testing.T, es lipapi.ManagedEventStream) {
	t.Helper()
	for {
		ev, err := es.Recv(context.Background())
		if err != nil {
			_ = es.Close()
			return
		}
		_ = ev
	}
}

func TestNewWSDialerCopiesHTTPTransportDialer(t *testing.T) {
	t.Parallel()
	var dialed bool
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("custom dialer used")
		},
	}}

	d := newWSDialer(client)
	if d.NetDialContext == nil {
		t.Fatal("websocket dialer did not copy http.Transport.DialContext")
	}
	_, err := d.NetDialContext(context.Background(), "tcp", "example.invalid:443")
	if err == nil {
		t.Fatal("expected custom dialer error")
	}
	if !dialed {
		t.Fatal("custom transport dialer was not used")
	}
}

// TestOpen_autoFallsBackOnWSStallWithinTimeout verifies that when the WebSocket
// upgrade succeeds but the server never sends a first event, auto transport does
// not hang: the first-event read deadline fires and the call falls back to HTTPS
// within the configured window. Not parallel: temporarily shortens the package
// var wsFirstEventTimeout.
//
//nolint:paralleltest
func TestOpen_autoFallsBackOnWSStallWithinTimeout(t *testing.T) {
	prev := wsFirstEventTimeout
	wsFirstEventTimeout = 200 * time.Millisecond
	t.Cleanup(func() { wsFirstEventTimeout = prev })

	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailureStall,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := New(Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             TransportAuto,
		ExperimentalWebSocket: true,
	})

	start := time.Now()
	es, err := be.Open(context.Background(), internalCodexCall(), internalCodexCand())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	drainUntilEnd(t, es)

	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https (auto must fall back when WS stalls before first event)", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fallback took %v, expected to be bounded by the short first-event timeout", elapsed)
	}
}

// TestOpen_autoFallsBackOnWSStallAfterLifecycleEvent verifies that response.created
// alone does not commit the WebSocket attempt. If WS stalls before text/tool output,
// auto transport must fall back to HTTPS instead of leaving the client with no content.
//
//nolint:paralleltest
func TestOpen_autoFallsBackOnWSStallAfterLifecycleEvent(t *testing.T) {
	prev := wsFirstEventTimeout
	wsFirstEventTimeout = 200 * time.Millisecond
	t.Cleanup(func() { wsFirstEventTimeout = prev })

	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "http-ok",
		ForcedWSFailure: refbackend.WSFailureStallAfterFirstEvent,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := New(Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             TransportAuto,
		ExperimentalWebSocket: true,
	})

	start := time.Now()
	es, err := be.Open(context.Background(), internalCodexCall(), internalCodexCand())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	drainUntilEnd(t, es)

	if got := srv.LatestRequest().Transport; got != "https" {
		t.Fatalf("transport = %q, want https (auto must fall back before committed output)", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fallback took %v, expected to be bounded by the short first-event timeout", elapsed)
	}
}

// TestOpen_wsFirstEventTimeoutZeroDoesNotPanic ensures clearing the deadline
// path is exercised by a normal WS success after the timeout var was mutated.
//
//nolint:paralleltest
func TestOpen_wsFirstEventTimeoutZeroDoesNotPanic(t *testing.T) {
	prev := wsFirstEventTimeout
	wsFirstEventTimeout = 5 * time.Second
	t.Cleanup(func() { wsFirstEventTimeout = prev })

	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ws-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := New(Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             TransportWebSocket,
		ExperimentalWebSocket: true,
	})
	es, err := be.Open(context.Background(), internalCodexCall(), internalCodexCand())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	drainUntilEnd(t, es)
	if got := srv.LatestRequest().Transport; got != "websocket" {
		t.Fatalf("transport = %q, want websocket", got)
	}
}

func TestWSStreamRecvContextCancelsAfterFirstEvent(t *testing.T) {
	t.Parallel()

	srv := refbackend.New(refbackend.Config{
		Token:           "sk-codex",
		OutputText:      "ws-stall",
		ForcedWSFailure: refbackend.WSFailureStallAfterFirstEvent,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	be := New(Config{
		BaseURL:               ts.URL + "/backend-api/codex",
		AccessToken:           "sk-codex",
		HTTPClient:            ts.Client(),
		Transport:             TransportWebSocket,
		ExperimentalWebSocket: true,
	})
	es, err := be.Open(context.Background(), internalCodexCall(), internalCodexCand())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	if _, err := es.Recv(context.Background()); err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := es.Recv(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Recv error = %v, want context deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Recv did not return after context deadline")
	}
}
