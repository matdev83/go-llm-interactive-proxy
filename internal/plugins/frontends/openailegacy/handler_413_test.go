package openailegacy_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

func TestHandler_requestBodyTooLarge_returns413(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	h := &openailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
	}
	body := bytes.Repeat([]byte("b"), int(reqbody.DefaultMaxBytes)+1)
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

func TestHandler_JSONGuardRejectsDeepBodyBeforeExecutor(t *testing.T) {
	t.Parallel()
	const marker = "MALICIOUS_MARKER"
	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
	body := strings.Repeat("[", 130) + `"` + marker + `"` + strings.Repeat("]", 130)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for deeply nested JSON")
	}
	if strings.Contains(rr.Body.String(), marker) {
		t.Fatalf("response echoed malicious marker: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid request JSON") {
		t.Fatalf("response body = %s, want invalid request JSON", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "could not read request body") {
		t.Fatalf("response body used read-error message: %s", rr.Body.String())
	}
}

func TestHandler_invalidJSONDoesNotEchoBodyMarker(t *testing.T) {
	t.Parallel()
	const marker = "MALICIOUS_MARKER"
	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"`+marker+`"}]`))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for invalid JSON")
	}
	if strings.Contains(rr.Body.String(), marker) {
		t.Fatalf("response echoed malicious marker: %s", rr.Body.String())
	}
}

func TestHandler_validMinimalRequestReachesExecutorAndEmitsOriginalBody(t *testing.T) {
	t.Parallel()
	exec := &recordingExecutor{}
	body := readGolden(t, "create_text_nonstream.json")
	obs := &captureObserver{}
	h := &openailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		TrafficPorts:         traffic.PortBundle{Obs: obs},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor was not called for valid request")
	}
	if obs.event.Leg != traffic.LegCTP {
		t.Fatalf("traffic leg: %q", obs.event.Leg)
	}
	if !bytes.Equal(obs.event.Body, body) {
		t.Fatalf("traffic body = %q, want original %q", obs.event.Body, body)
	}
}

type recordingExecutor struct{ called bool }

func (e *recordingExecutor) Execute(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	e.called = true
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

func (e *recordingExecutor) CancelALeg(context.Context, lipapi.ALegCancelRequest) error { return nil }

func (e *recordingExecutor) WallClock() func() time.Time { return nil }

type captureObserver struct{ event traffic.Observation }

func (o *captureObserver) OnObservation(_ context.Context, event traffic.Observation) error {
	o.event = event
	return nil
}
