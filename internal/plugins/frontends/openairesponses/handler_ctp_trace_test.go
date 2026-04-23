package openairesponses_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	front "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

type captureTrafficObs struct {
	mu   sync.Mutex
	last traffic.Observation
}

func (c *captureTrafficObs) OnObservation(_ context.Context, ev traffic.Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = ev
	return nil
}

func TestHandler_LegCTP_traceMatchesStableCallID(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	cap := &captureTrafficObs{}
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	h := &front.Handler{
		Exec:                 ex,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		TrafficPorts:         traffic.PortBundle{Obs: cap},
	}
	decoded, err := front.DecodeCreateRequest(body, front.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	wantTrace := diag.StableCallID(decoded.Call)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}

	cap.mu.Lock()
	got := cap.last
	cap.mu.Unlock()
	if got.TraceID != wantTrace {
		t.Fatalf("CTP TraceID: got %q want %q", got.TraceID, wantTrace)
	}
	if got.Leg != traffic.LegCTP {
		t.Fatalf("leg: got %q want client_to_proxy", got.Leg)
	}
}
