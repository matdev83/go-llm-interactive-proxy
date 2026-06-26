package openaicodex_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/openaicodex"
	refbackend "github.com/matdev83/go-llm-interactive-proxy/internal/refbackend/openaicodex"
)

const gpt55FreePlan400Body = `{"error":{"message":"gpt-5.5 is not available on free plan","type":"invalid_request_error"}}`

func requestModel(t *testing.T, srv *refbackend.Server) string {
	t.Helper()
	model, ok := srv.LatestRequest().Body["model"].(string)
	if !ok || model == "" {
		t.Fatalf("missing model in body: %#v", srv.LatestRequest().Body)
	}
	return model
}

func TestGPT55Downgrade_proactiveManagedFreePlanSendsTargetModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "free.json", managedAccountFixture{
		AccountID:   "acct-free",
		AccessToken: "tok-free",
		QuotaHeaders: map[string]string{
			"x-codex-plan-type": "free",
		},
	})

	srv := refbackend.New(refbackend.Config{Token: "tok-free", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = ts.URL + "/backend-api/codex"
	cfg.HTTPClient = ts.Client()
	be := backend.New(cfg)
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := requestModel(t, srv); got != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", got)
	}
}

func TestGPT55Downgrade_noProactiveDowngradeForProPlan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAccountFile(t, dir, "pro.json", managedAccountFixture{
		AccountID:   "acct-pro",
		AccessToken: "tok-pro",
		QuotaHeaders: map[string]string{
			"x-codex-plan-type": "pro",
		},
	})

	srv := refbackend.New(refbackend.Config{Token: "tok-pro", OutputText: "ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cfg := managedOAuthCfg(dir)
	cfg.BaseURL = ts.URL + "/backend-api/codex"
	cfg.HTTPClient = ts.Client()
	be := backend.New(cfg)
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if got := requestModel(t, srv); got != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", got)
	}
}

func TestGPT55Downgrade_reactive400FreePlanRetriesWithTarget(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var lastModel atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		model, _ := payload["model"].(string)
		lastModel.Store(model)
		if model == "gpt-5.5" {
			http.Error(w, gpt55FreePlan400Body, http.StatusBadRequest)
			return
		}
		refbackend.New(refbackend.Config{Token: "tok"}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:     srv.URL + "/backend-api/codex",
		AccessToken: "tok",
		HTTPClient:  srv.Client(),
	})
	es, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(t, es)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	got, ok := lastModel.Load().(string)
	if !ok {
		t.Fatal("lastModel not string")
	}
	if got != "gpt-5.4" {
		t.Fatalf("final model = %q, want gpt-5.4", got)
	}
}

func TestGPT55Downgrade_disabledReturns400WithoutRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		http.Error(w, gpt55FreePlan400Body, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:                srv.URL + "/backend-api/codex",
		AccessToken:            "tok",
		HTTPClient:             srv.Client(),
		GPT55DowngradeDisabled: true,
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.5"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestGPT55Downgrade_nonSourceModelDoesNotDowngrade(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		http.Error(w, gpt55FreePlan400Body, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL:     srv.URL + "/backend-api/codex",
		AccessToken: "tok",
		HTTPClient:  srv.Client(),
	})
	_, err := be.Open(context.Background(), sampleCall(), routing.AttemptCandidate{
		Primary: routing.Primary{Model: "gpt-5.3-codex"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}
