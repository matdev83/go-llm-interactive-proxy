package openairesponses_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

func TestHandler_requestBodyTooLarge_returns413(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{
		Exec:                 nil,
		DefaultRouteSelector: "stub:gpt-4o-mini",
	}
	body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: %q", ct)
	}
}

func TestHandler_configuredMaxBody_returns413(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{
		Exec:                 nil,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		MaxRequestBodyBytes:  50,
	}
	body := bytes.Repeat([]byte("b"), 51)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}
