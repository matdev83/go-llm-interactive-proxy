package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	corehttp "github.com/matdev83/go-llm-interactive-proxy/internal/core/http"
)

func TestTraceMiddleware_propagatesHeader(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := diag.TraceID(r.Context())
		if got != "trace-123" {
			t.Fatalf("expected trace-123, got %q", got)
		}
	})

	h := corehttp.TraceMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace-ID", "trace-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
}

func TestRequestIDMiddleware_generatesWhenMissing(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := diag.TraceID(r.Context())
		if got == "" {
			t.Fatal("expected trace ID")
		}
	})

	h := corehttp.RequestIDMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Trace-ID"); got != "t_00000001" {
		t.Fatalf("X-Trace-ID = %q, want t_00000001", got)
	}
}

func TestRequestIDMiddleware_preservesExisting(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := diag.TraceID(r.Context())
		if got != "existing-id" {
			t.Fatalf("expected existing-id, got %q", got)
		}
	})

	ctx := diag.WithTraceID(context.Background(), "existing-id")
	h := corehttp.RequestIDMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
}
