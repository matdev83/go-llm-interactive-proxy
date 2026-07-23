package stdhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/frontends/gemini"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestMountBundledFrontends_geminiDoesNotRegisterRoot(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	var healthCalled bool
	mux.HandleFunc("/healthz", func(http.ResponseWriter, *http.Request) {
		healthCalled = true
	})
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	plugins := []config.PluginConfig{
		{ID: gemini.ID, Enabled: true},
	}
	if err := MountBundledFrontends(MountBundledFrontendsInput{Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:gemini-2.0-flash",
			Plugins:              plugins,
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !healthCalled {
		t.Fatal("expected /healthz to hit explicit handler, not Gemini")
	}
}

func TestTokenAccountingAdminMountedWithDiagnosticsSecret(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	svc := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = svc
	built := &runtimebundle.Built{
		Executor:             ex,
		PluginRegistry:       pluginreg.NewRegistry(),
		TokenAccountingAdmin: svc,
	}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody())))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status %d body=%s", missing.Code, missing.Body.String())
	}

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status %d body=%s", allowed.Code, allowed.Body.String())
	}
	if !strings.Contains(allowed.Body.String(), `"client_visible"`) {
		t.Fatalf("response missing count body: %s", allowed.Body.String())
	}
	if strings.Contains(allowed.Body.String(), "secret prompt") {
		t.Fatalf("response leaked request content: %s", allowed.Body.String())
	}
}

func TestTokenAccountingAdminDisabledNotRegistered(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(false)
	ex := runtime.TestExecutor()
	built := &runtimebundle.Built{Executor: ex, PluginRegistry: pluginreg.NewRegistry()}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTokenAccountingAdminMountedBodyLimitDoesNotEchoContent(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	cfg.Accounting.Admin.MaxBodyBytes = 16
	svc := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = svc
	built := &runtimebundle.Built{
		Executor:             ex,
		PluginRegistry:       pluginreg.NewRegistry(),
		TokenAccountingAdmin: svc,
	}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret prompt") || strings.Contains(rr.Body.String(), "Messages") {
		t.Fatalf("response leaked request content: %s", rr.Body.String())
	}
}

func TestTokenAccountingAdmin_explicitServicePreferredOverExecutorFallback(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	explicit := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	fallback := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, failingLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = fallback
	built := &runtimebundle.Built{
		Executor:             ex,
		PluginRegistry:       pluginreg.NewRegistry(),
		TokenAccountingAdmin: explicit,
	}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("explicit service status %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTokenAccountingAdmin_executorFallbackWhenExplicitNil(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	fallback := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = fallback
	built := &runtimebundle.Built{
		Executor:       ex,
		PluginRegistry: pluginreg.NewRegistry(),
		// TokenAccountingAdmin intentionally nil → executor AdminCountService fallback.
	}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("fallback status %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"client_visible"`) {
		t.Fatalf("response missing count body: %s", rr.Body.String())
	}
}

func TestTokenAccountingAdmin_nilServiceReturnsUnavailable(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	ex := runtime.TestExecutor()
	// No TokenAccountingAdmin and no AdminCountService.
	built := &runtimebundle.Built{Executor: ex, PluginRegistry: pluginreg.NewRegistry()}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "count_unavailable") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

type failingLocalCounter struct{}

func (failingLocalCounter) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, errors.New("fallback must not be used")
}
func (failingLocalCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, errors.New("fallback must not be used")
}
func (failingLocalCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, errors.New("fallback must not be used")
}

func tokenAccountingAdminTestConfig(enabled bool) *config.Config {
	return &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:model"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz", SharedSecret: "secretsecret"},
		Accounting: config.AccountingConfig{
			Enabled: true,
			Mode:    "local_only",
			Admin:   config.AccountingAdminConfig{Enabled: enabled, Path: "/admin/token-count", MaxBodyBytes: 1024},
		},
		Plugins: config.PluginsConfig{},
	}
}

func mustRuntimeApp(t *testing.T, cfg *config.Config) *runtime.App {
	t.Helper()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func tokenAccountingAdminBody() string {
	return `{"backend":"stub","model":"model","call":{"ID":"call-1","Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"secret prompt"}]}]}}`
}

type fixedLocalCounter struct{}

func (fixedLocalCounter) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 1, TotalTokens: 1, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated}}, nil
}

func (fixedLocalCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 7, TotalTokens: 7, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated}}, nil
}

func (fixedLocalCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated}}, nil
}

func TestMountBundledFrontends_explicitRegistryMissingFrontend(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ex := testkit.NewStubExecutor(t, lipapi.NewBackendCaps(lipapi.CapabilityStreaming), "ok", nil)
	err := MountBundledFrontends(MountBundledFrontendsInput{Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:x",
			Plugins:              []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	})
	if err == nil {
		t.Fatal("expected error when registry lacks frontend factories")
	}
	if !strings.Contains(err.Error(), `unknown frontend plugin "openai-responses"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewStandardHandler_diagnosticsHealthzMounted locks the diagnostics mount block in
// prepareStandardHandler: when Diagnostics.Enabled is true with a HealthPath, GET <health>
// returns 200 through the assembled standard handler. Characterization for the
// internal/stdhttp/server.go concern split (arch review Task 1.3/1.4).
func TestNewStandardHandler_diagnosticsHealthzMounted(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:model"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz", SharedSecret: "secretsecret"},
		Plugins:     config.PluginsConfig{},
	}
	ex := runtime.TestExecutor()
	built := &runtimebundle.Built{Executor: ex, PluginRegistry: pluginreg.NewRegistry()}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestNewStandardHandler_openAIModelsAndModelRegistryDiagMounted(t *testing.T) {
	t.Parallel()
	const secret = "secretsecret"
	provider := &mutableInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-4o",
		NativeID:    "native-hidden",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "openai-a",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "openai-responses:gpt-4o"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			SharedSecret: secret,
		},
		ModelInventory: config.ModelInventoryConfig{
			DiagnosticsPath: "/debug/model-registry",
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
		},
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	built := &runtimebundle.Built{
		Executor:             ex,
		PluginRegistry:       reg,
		ModelRegistryRuntime: rt,
		ModelRegistry:        rt.ActiveRegistry(),
	}
	app := mustRuntimeApp(t, cfg)
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/v1/models status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "native-hidden") {
		t.Fatalf("native leak: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "openai-a:openai/gpt-4o") {
		t.Fatalf("missing pinned id: %s", rr.Body.String())
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("/v1/models missing ETag")
	}
	req304 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req304.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req304)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("/v1/models If-None-Match status %d want 304", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/debug/model-registry", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unauthed model-registry status %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/model-registry", nil)
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("authed model-registry status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "native-hidden") {
		t.Fatalf("native leak in diag: %s", rr.Body.String())
	}
}
