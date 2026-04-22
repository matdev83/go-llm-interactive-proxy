package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewRuntimeClient_staticCredsAndEndpoint(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	}
	cli, err := newRuntimeClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewRuntimeClient_nilContextUsesBackground(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg := Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	}
	cli, err := newRuntimeClient(nil, cfg) //nolint:staticcheck // nil ctx exercises documented fallback to context.Background
	if err != nil {
		t.Fatal(err)
	}
	if cli == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewWithContext_nilAppliesDefaultLoadDeadline(t *testing.T) {
	t.Parallel()
	b := NewWithContext(nil, Config{ //nolint:staticcheck // nil ctx: load uses DefaultLoadConfigTimeout
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
	})
	if b.Open == nil {
		t.Fatal("expected backend with Open")
	}
}

func TestNewWithContext_parentWithDeadlineSkipsChildTimeout(t *testing.T) {
	t.Parallel()
	// httptest + static creds so config load stays local; use generous parent deadline
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := NewWithContext(ctx, Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
	})
	if b.Open == nil {
		t.Fatal("expected backend with Open")
	}
}
