package acp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
