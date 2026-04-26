package runtimebundle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

func TestBuild_continuityPostgres_unreachableDoesNotFallback(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	const secret = "NEVER_EMBED_THIS_SECRET"
	dsn := "postgres://u:" + secret + "@127.0.0.1:1/nosuch?sslmode=disable"
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: dsn,
		},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked password: %s", msg)
	}
	if !strings.Contains(msg, "continuity") {
		t.Fatalf("want continuity context, got: %s", msg)
	}
}

func TestBuild_startupContextCanceled_continuityPostgres(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	startupCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: "postgres://u:p@127.0.0.1:59999/nosuch?sslmode=disable",
		},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		StartupContext: startupCtx,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled in error chain, got: %v", err)
	}
}

func TestBuild_disposesContinuityWhenSecureSessionFails(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	dbpath := filepath.Join(tmp, "continuity.db")
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: dbpath,
		},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         "postgres://x:y@127.0.0.1:1/db?sslmode=disable",
			TokenFingerprintKey: testSecureKey32,
		},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	if rmErr := os.Remove(dbpath); rmErr != nil {
		t.Fatalf("continuity sqlite file should be released after failed secure_session open: %v", rmErr)
	}
}

func TestBuild_secureSessionPostgres_unreachableDurableAuditDoesNotFallback(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	const secret = "NEVER_EMBED_SS_SECRET"
	dsn := "postgres://u:" + secret + "@127.0.0.1:1/nosuch?sslmode=disable"
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         dsn,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "durable",
		},
	}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked password: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "secure_session") {
		t.Fatalf("want secure_session context, got: %s", msg)
	}
}
