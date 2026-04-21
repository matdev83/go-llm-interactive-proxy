package anthropic_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/anthropic"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

func TestHandler_requestBodyTooLarge_returns413(t *testing.T) {
	t.Parallel()
	h := &anthropic.Handler{
		Exec:                 nil,
		DefaultRouteSelector: "stub:claude",
	}
	body := bytes.Repeat([]byte("c"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}
