package acp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
)

func TestNewHTTPTransport_nilClient_usesHTTPClientStandard(t *testing.T) {
	t.Parallel()

	tr, err := newHTTPTransport("http://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tr.hc == nil {
		t.Fatal("expected non-nil http.Client")
	}
	want := httpclient.Standard()
	if tr.hc.Timeout != want.Timeout {
		t.Fatalf("timeout: got %v want %v", tr.hc.Timeout, want.Timeout)
	}
	gotT, ok := tr.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport: got %T want *http.Transport", tr.hc.Transport)
	}
	wantT, ok := want.Transport.(*http.Transport)
	if !ok {
		t.Fatal("httpclient.Standard Transport is not *http.Transport")
	}
	if gotT.TLSHandshakeTimeout != wantT.TLSHandshakeTimeout ||
		gotT.MaxIdleConns != wantT.MaxIdleConns ||
		gotT.ForceAttemptHTTP2 != wantT.ForceAttemptHTTP2 {
		t.Fatalf("transport policy mismatch: got TLS=%v idle=%v h2=%v want TLS=%v idle=%v h2=%v",
			gotT.TLSHandshakeTimeout, gotT.MaxIdleConns, gotT.ForceAttemptHTTP2,
			wantT.TLSHandshakeTimeout, wantT.MaxIdleConns, wantT.ForceAttemptHTTP2)
	}
}

func TestNewHTTPTransport_preservesProvidedClient(t *testing.T) {
	t.Parallel()

	custom := &http.Client{Timeout: 7 * time.Second}
	tr, err := newHTTPTransport("http://example.com", custom)
	if err != nil {
		t.Fatal(err)
	}
	if tr.hc != custom {
		t.Fatal("expected provided *http.Client instance to be preserved")
	}
	if tr.hc.Timeout != 7*time.Second {
		t.Fatalf("timeout mutated: got %v want %v", tr.hc.Timeout, 7*time.Second)
	}
}

func TestHTTPTransport_CallUnary_rejectsOversizedBody(t *testing.T) {
	t.Parallel()

	const over = maxUnaryHTTPResponseBytes + 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		src := io.LimitReader(bytes.NewReader(bytes.Repeat([]byte("x"), over)), int64(over))
		_, _ = io.Copy(w, src)
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.CallUnary(context.Background(), []byte(`{}`), http.StatusOK)
	if err == nil {
		t.Fatal("expected error for oversized unary response body")
	}
	if !strings.Contains(err.Error(), "response body exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPTransport_CallUnary_okWithinLimit(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte(`{"jsonrpc":"2.0","result":{}}`), 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	got, err := tr.CallUnary(context.Background(), []byte(`{}`), http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("body mismatch: len(got)=%d want %d", len(got), len(payload))
	}
}

func TestHTTPTransport_CallUnary_httpErrorUsesLimitedBody(t *testing.T) {
	t.Parallel()

	body := bytes.Repeat([]byte("e"), 6000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	tr, err := newHTTPTransport(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.CallUnary(context.Background(), []byte(`{}`), http.StatusOK)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if len(msg) > maxErrorSnippetBytes+300 {
		t.Fatalf("HTTP error diagnostics unexpectedly large: %d", len(msg))
	}
	if !strings.Contains(msg, "HTTP 400") {
		t.Fatalf("expected status in error: %s", msg)
	}
}
