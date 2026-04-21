package gemini_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

func TestHandler_requestBodyTooLarge_returns413(t *testing.T) {
	t.Parallel()
	h := &gemini.Handler{
		Exec:                 nil,
		DefaultRouteSelector: "stub:gemini-2.0-flash",
	}
	body := bytes.Repeat([]byte("d"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
}
