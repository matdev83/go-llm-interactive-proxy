package stdhttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// unifiedPolicyRoute is a single configured default_route used to prove all bundled HTTP
// frontends consume the same policy value when the client omits X-LIP-Route (task 10.4).
const unifiedPolicyRoute = "stub:unified-policy-model"

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func policyConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{
			DefaultRoute: unifiedPolicyRoute,
		},
	}
}

func TestOmittedRoute_openaiResponses_usesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	route := routing.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	if route != unifiedPolicyRoute {
		t.Fatalf("effective route %q", route)
	}
	var cap sync.Map
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", &cap)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(mux, ex, route, []config.PluginConfig{{ID: "openai-responses", Enabled: true}}, 0, reg); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := cap.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustLIPCall(t, v)
	if got := call.Route.Selector; got != unifiedPolicyRoute {
		t.Fatalf("route selector %q want %q", got, unifiedPolicyRoute)
	}
}

func TestOmittedRoute_openaiLegacy_usesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	route := routing.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	var cap sync.Map
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", &cap)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(mux, ex, route, []config.PluginConfig{{ID: "openai-legacy", Enabled: true}}, 0, reg); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := cap.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustLIPCall(t, v)
	if got := call.Route.Selector; got != unifiedPolicyRoute {
		t.Fatalf("route selector %q want %q", got, unifiedPolicyRoute)
	}
}

func TestOmittedRoute_anthropic_usesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	route := routing.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	var cap sync.Map
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", &cap)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(mux, ex, route, []config.PluginConfig{{ID: "anthropic", Enabled: true}}, 0, reg); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := cap.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustLIPCall(t, v)
	if got := call.Route.Selector; got != unifiedPolicyRoute {
		t.Fatalf("route selector %q want %q", got, unifiedPolicyRoute)
	}
}

func TestBuildExecutor_defaultBackendFromEffectiveRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	cfg.Continuity = config.ContinuityConfig{InMemory: true}
	exec, _, _, err := BuildExecutor(cfg, nil, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	if exec.DefaultBackend != "stub" {
		t.Fatalf("DefaultBackend %q want stub", exec.DefaultBackend)
	}
}
