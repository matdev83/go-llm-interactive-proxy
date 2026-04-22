package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestNewWithContext_nilUsesBackground(t *testing.T) {
	t.Parallel()
	b := NewWithContext(nil, Config{ //nolint:staticcheck // nil ctx exercises documented fallback to context.Background
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
	})
	if b.Open == nil {
		t.Fatal("expected backend with Open")
	}
}
