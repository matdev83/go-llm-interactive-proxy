package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/controlplane/ledgerstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	cp "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func controlPlaneMountConfig(queryEnabled bool) *config.Config {
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:model"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			SharedSecret: "secretsecret",
		},
		Plugins: config.PluginsConfig{},
	}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	cfg.ControlPlane.Query.Enabled = queryEnabled
	cfg.ControlPlane.Query.PathPrefix = "/cp"
	return cfg
}

func controlPlaneBuilt(t *testing.T, cfg *config.Config) *runtimebundle.Built {
	t.Helper()
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	queries := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled:         cfg.ControlPlane.Query.Enabled,
		DefaultPageSize: cfg.ControlPlane.Query.DefaultPageSize,
		MaxPageSize:     cfg.ControlPlane.Query.MaxPageSize,
	})
	ex := runtime.TestExecutor()
	return &runtimebundle.Built{
		Executor:            ex,
		PluginRegistry:      pluginreg.NewRegistry(),
		ControlPlaneQueries: queries,
		ControlPlaneStatus:  status,
	}
}

func TestControlPlaneQuery_MountedWhenEnabledAndProtected(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(true)
	built := controlPlaneBuilt(t, cfg)
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	// Without shared secret -> 403 (protected).
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/cp/status", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret: got %d, want 403", missing.Code)
	}

	// With shared secret -> 200 and ready status.
	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cp/status", nil)
	req.Header.Set("X-LIP-Diagnostics-Secret", "secretsecret")
	h.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status: got %d, want 200 body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestControlPlaneQuery_NotMountedWhenDisabled(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(false)
	built := controlPlaneBuilt(t, cfg)
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, slog.Default(), built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanup(context.Background()) })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cp/status", nil)
	req.Header.Set("X-LIP-Diagnostics-Secret", "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled: got %d, want 404", rr.Code)
	}
}
