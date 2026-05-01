package stdhttp

import (
	"context"
	"strings"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// RunWithRuntime must reject unsafe diagnostics posture before requiring a built runtime.
//
//nolint:paralleltest // documents coupling to validateStartupSecurity ordering; keep sequential.
func TestRunWithRuntime_rejectsUnsafeDiagnosticsPostureBeforeBuilt(t *testing.T) {
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Access:     coreconfig.AccessConfig{Mode: "multi_user"},
		Auth:       coreconfig.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     coreconfig.ServerConfig{Address: "10.0.0.1:0", AuthMode: coreconfig.AuthModeExternal},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Backends: []coreconfig.PluginConfig{{ID: "b1", Enabled: true}},
		},
		Diagnostics: coreconfig.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			AttemptsPath: "/admin/attempts",
			SharedSecret: "",
		},
	}
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	err = RunWithRuntime(ctx, cfg, app, log, nil)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") {
		t.Fatalf("want posture error before built checks, got %v", err)
	}
}
