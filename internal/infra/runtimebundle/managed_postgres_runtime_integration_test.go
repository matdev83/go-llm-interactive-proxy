//go:build integration

package runtimebundle_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

// TestBuild_postgresBothStores_closersAndNoMigration requires PostgreSQL; see testkit.LIPTestPostgresDSN
// (or legacy testkit.LIPManagedPostgresDSN).
// Req 5.5–5.6: wiring does not add cross-product migration; each store owns its handle.
func TestBuild_postgresBothStores_closersAndNoMigration(t *testing.T) {
	t.Parallel()
	dsn, ok := testkit.PostgresTestDSN()
	if !ok {
		t.Skipf("set %s (or legacy %s) to run integration test", testkit.LIPTestPostgresDSN, testkit.LIPManagedPostgresDSN)
	}
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Database: config.DatabaseConfig{
			MaxOpenConns: 2,
		},
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: dsn,
		},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         dsn,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "durable",
		},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Closers) != 2 {
		t.Fatalf("want 2 closers (continuity + secure_session), got %d", len(b.Closers))
	}
	for i, c := range b.Closers {
		if err := c(); err != nil {
			t.Fatalf("closer %d: %v", i, err)
		}
	}
}
