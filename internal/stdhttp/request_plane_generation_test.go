package stdhttp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"gopkg.in/yaml.v3"
)

func TestComposeRequestPlane_RouteConflictRejects(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubPlaneConfig(t, "rc", "x", "rc:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
			{ID: "responses-dup", Kind: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeRequestPlane,
	})
	if err == nil {
		t.Fatal("expected route conflict")
	}
	if !errors.Is(err, stdhttp.ErrRouteConflict) {
		t.Fatalf("want ErrRouteConflict, got %v", err)
	}
}

func TestComposeRequestPlane_ManagementRoutesNotMounted(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubPlaneConfig(t, "mgmt", "m", "mgmt:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	for _, path := range []string{"/admin/config/reload", "/admin/config/status"} {
		rr := httptest.NewRecorder()
		bundle.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s must stay outside swappable graph: %d", path, rr.Code)
		}
	}
}

func TestGenerationDispatcher_CoexistOldNewHandlers(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	oldBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: stubPlaneConfig(t, "old", "OLD", "old:stub-default", []config.PluginConfig{{ID: "openai-responses", Enabled: true}}),
		Compose:   stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatal(err)
	}
	newBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: stubPlaneConfig(t, "new", "NEW", "new:stub-default", []config.PluginConfig{{ID: "openai-responses", Enabled: true}}),
		Compose:   stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr := runtimehost.NewManager(4, nil)
	disp := runtimehost.NewGenerationDispatcher(mgr)
	if err := mgr.Publish(mgr.PrepareRequestPlane("old", oldBundle)); err != nil {
		t.Fatal(err)
	}
	lease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	defer lease.Release()
	if err := mgr.Publish(mgr.PrepareRequestPlane("new", newBundle)); err != nil {
		t.Fatal(err)
	}

	bodyOld := postPlaneResponses(t, lease.Handler(), "stub-default")
	bodyNew := postPlaneResponses(t, disp, "stub-default")
	if !strings.Contains(bodyOld, "OLD") || strings.Contains(bodyOld, "NEW") {
		t.Fatalf("old=%s", bodyOld)
	}
	if !strings.Contains(bodyNew, "NEW") || strings.Contains(bodyNew, "OLD") {
		t.Fatalf("new=%s", bodyNew)
	}
}

func newStdProcess(t *testing.T) *runtimebundle.ProcessServices {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func stubPlaneConfig(t *testing.T, backendID, text, defaultRoute string, frontends []config.PluginConfig) *config.Config {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(fmt.Sprintf("text: %q\ninput_tokens: 1\noutput_tokens: 1\n", text)), &n); err != nil {
		t.Fatal(err)
	}
	for n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = *n.Content[0]
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: defaultRoute},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: frontends,
			Backends: []config.PluginConfig{{
				Kind: "local-stub", ID: backendID, Enabled: true, Config: n,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

func postPlaneResponses(t *testing.T, h http.Handler, model string) string {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"model":%q,"stream":false,"input":[{"role":"user","content":"ping"}]}`, model))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}
