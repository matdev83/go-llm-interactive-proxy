package openairesponses_test

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/decodeqos"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/reqbody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/routeselect"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// Task 1.2 characterization: freeze OpenAI Responses outer + shared pipe order
// (requirements 1, 2, 17; design 1, 2, 14, 16).
// Shared oracle: body read -> header selector -> optional whole-body resolver
// (absent here) -> shared preflight -> TryAdmit -> guarded RouteFromBodyModel/
// Decode -> post-decode/traffic -> execute. No legacy ResolveRouteSelector is
// configured; RouteFromBodyModel=true with header-wins precedence.

type orderingRouteExec struct {
	called   bool
	selector string
}

func (e *orderingRouteExec) Execute(_ context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	e.called = true
	if call != nil {
		e.selector = call.Route.Selector
	}
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "ok"},
		{Kind: lipapi.EventResponseFinished},
	}), nil
}

func (e *orderingRouteExec) CancelALeg(context.Context, lipapi.ALegCancelRequest) error { return nil }
func (e *orderingRouteExec) WallClock() func() time.Time                                { return nil }

func TestOpenAIResponses_OrderingMethodPathBodyAdmissionPreflightPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("method before path", func(t *testing.T) {
		t.Parallel()
		h := &openairesponses.Handler{Exec: &orderingRouteExec{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
		req := httptest.NewRequest(http.MethodGet, "/v1/other", strings.NewReader(`{"model":"x"}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", rr.Code)
		}
	})

	t.Run("path before body", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openairesponses.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/v1/other", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 (unknown path owns request even for oversized body)", rr.Code)
		}
		if exec.called {
			t.Fatal("executor ran after path reject")
		}
	})

	t.Run("body before admission", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingRouteExec{}
		h := &openairesponses.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
		body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413 (body limit owns oversized body even when admission saturated)", rr.Code)
		}
		if exec.called {
			t.Fatal("executor ran for oversized body")
		}
	})

	t.Run("preflight before admission", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingRouteExec{}
		h := &openairesponses.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 (shared preflight owns malformed JSON even when admission saturated)", rr.Code)
		}
		msg, _ := decodeOpenAIAPIError(t, rr.Body.Bytes())
		if msg != "invalid request JSON" {
			t.Fatalf("message=%q want invalid request JSON", msg)
		}
		if exec.called {
			t.Fatal("executor ran after preflight failure")
		}
	})

	t.Run("admission before decode", func(t *testing.T) {
		t.Parallel()
		limiter := decodeqos.New(1, math.MaxInt64)
		release, ok, err := limiter.TryAcquire(t.Context(), 0)
		if err != nil || !ok {
			t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
		}
		defer release()
		exec := &orderingRouteExec{}
		h := &openairesponses.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("status=%d want 429", rr.Code)
		}
		if got := rr.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
			t.Fatalf("Retry-After=%q want %q", got, decodeqos.RetryAfterSeconds)
		}
		if exec.called {
			t.Fatal("executor ran after admission reject")
		}
	})

	t.Run("decode failure before execute", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := &openairesponses.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rr.Code)
		}
		if exec.called {
			t.Fatal("executor ran after decode failure")
		}
	})
}

func TestOpenAIResponses_HeaderSelectorPrecedenceAndBodyDefault(t *testing.T) {
	t.Parallel()
	newHandler := func(exec *orderingRouteExec) *openairesponses.Handler {
		return &openairesponses.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:default",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		}
	}

	t.Run("header wins over body model", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"stub:from-body","input":"hi"}`))
		req.Header.Set(openairesponses.HeaderRouteSelector, "stub:from-header")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if exec.selector != "stub:from-header" {
			t.Fatalf("selector=%q want header value", exec.selector)
		}
	})

	t.Run("body model used when header absent", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"stub:from-body","input":"hi"}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if exec.selector != "stub:from-body" {
			t.Fatalf("selector=%q want body model", exec.selector)
		}
	})

	t.Run("unknown model falls back to default", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		// Golden model gpt-4o-mini carries no stub prefix, so RouteFromBodyModel
		// must fall back to the configured default rather than an empty selector.
		if exec.selector != "stub:default" {
			t.Fatalf("selector=%q want default", exec.selector)
		}
	})

	t.Run("no legacy whole-body resolver bypass: header still wins", func(t *testing.T) {
		t.Parallel()
		exec := &orderingRouteExec{}
		h := newHandler(exec)
		// A body carrying an extra opaque field must not change selector beyond
		// the header/body-model/default rule. A legacy full-body resolver would
		// be free to interpret this field; the certified lane must ignore it.
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"stub:from-body","input":"hi","x_route_hint":"stub:evil"}`))
		req.Header.Set(openairesponses.HeaderRouteSelector, "stub:from-header")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if exec.selector != "stub:from-header" {
			t.Fatalf("selector=%q want header (opaque body hint must not bypass precedence)", exec.selector)
		}
	})
}

func TestOpenAIResponses_TrafficCarriesOriginalBodyBeforeExecute(t *testing.T) {
	t.Parallel()
	exec := &orderingRouteExec{}
	body := readGolden(t, "create_text_nonstream.json")
	obs := &captureObserver{}
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		TrafficPorts:         traffic.PortBundle{Obs: obs},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor not called")
	}
	if obs.event.Leg != traffic.LegCTP {
		t.Fatalf("traffic leg=%q want client_to_proxy", obs.event.Leg)
	}
	if !bytes.Equal(obs.event.Body, body) {
		t.Fatalf("traffic body=%q want original %q", obs.event.Body, body)
	}
}
