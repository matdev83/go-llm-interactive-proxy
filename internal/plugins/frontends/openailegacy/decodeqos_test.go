package openailegacy_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
)

func TestHandler_decodeLimiterSaturationReturns503WithoutExecutor(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.NewLimiter(1)
	release, ok, err := limiter.TryAcquire(t.Context())
	if err != nil || !ok {
		t.Fatalf("pre-acquire limiter: ok=%v err=%v", ok, err)
	}
	defer release()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeLimiter: limiter}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called while decode limiter was saturated")
	}
}

func TestHandler_oversizedBodyReturns413WithoutAcquiringDecodeLimiter(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.NewLimiter(1)
	release, ok, err := limiter.TryAcquire(t.Context())
	if err != nil || !ok {
		t.Fatalf("pre-acquire limiter: ok=%v err=%v", ok, err)
	}
	defer release()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeLimiter: limiter}
	body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for oversized body")
	}
}

func TestHandler_nilDecodeLimiterStillReachesExecutor(t *testing.T) {
	t.Parallel()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor was not called with nil decode limiter")
	}
}

func TestHandler_decodeLimiterReleasesAfterDecodeFailureAndSuccess(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.NewLimiter(1)
	h := &openailegacy.Handler{Exec: &recordingExecutor{}, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeLimiter: limiter}

	badReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"`))
	badRR := httptest.NewRecorder()
	h.ServeHTTP(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d body: %s", badRR.Code, badRR.Body.String())
	}

	goodReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
	goodRR := httptest.NewRecorder()
	h.ServeHTTP(goodRR, goodReq)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("good status: %d body: %s", goodRR.Code, goodRR.Body.String())
	}

	release, ok, err := limiter.TryAcquire(t.Context())
	if err != nil || !ok {
		t.Fatalf("limiter remained held after success: ok=%v err=%v", ok, err)
	}
	release()
}
