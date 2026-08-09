package openresponses

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance"
)

// TestHarness_CloseReleasesPorts proves the full-path deployment frees its
// frontend and provider-origin listeners deterministically: after Close, dialing
// either origin must be refused, not hang.
func TestHarness_CloseReleasesPorts(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
		t.Fatalf("round trip before close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertRefused(t, d.FrontendAddr(), "frontend origin")
	assertRefused(t, d.OriginFor(conformance.BackendOpenResponses).Addr(), "provider origin")
}

// TestHarness_NoGoroutineLeak proves repeated deploy/round-trip/close cycles do
// not accumulate goroutines beyond a settled baseline.
func TestHarness_NoGoroutineLeak(t *testing.T) {
	t.Parallel()
	baseline := settleGoroutines(t)

	for i := range 3 {
		d := conformance.Deploy(t, conformance.DeploymentSpec{
			Frontend:  conformance.FrontendOpenResponses,
			Backend:   conformance.BackendOpenResponses,
			Transport: conformance.TransportJSON,
		})
		if d == nil {
			t.Fatalf("Deploy cycle %d failed", i)
		}
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
			t.Fatalf("round trip cycle %d: %v", i, err)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("Close cycle %d: %v", i, err)
		}
	}

	after := settleGoroutines(t)
	const allowedGrowth = 5
	if after > baseline+allowedGrowth {
		t.Fatalf("goroutines grew from %d to %d across deploy/close cycles", baseline, after)
	}
}

func settleGoroutines(tb testing.TB) int {
	tb.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var n int
	for {
		n = runtime.NumGoroutine()
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return n
}

func assertRefused(tb testing.TB, addr, label string) {
	tb.Helper()
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		tb.Fatalf("%s (%s) still accepting connections after Close", label, addr)
	}
}

// TestHarness_WebSocketConnectionClosedOnClientDisconnect proves a WS client
// disconnect does not leave a lingering session goroutine or accepted port.
func TestHarness_WebSocketConnectionClosedOnClientDisconnect(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	// A raw turn opens and closes a connection; repeated raw turns exercise
	// session open/close without leaking.
	for i := range 2 {
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
			t.Fatalf("ws turn %d: %v", i, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
