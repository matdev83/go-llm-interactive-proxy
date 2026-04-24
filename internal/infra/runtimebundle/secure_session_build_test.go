package runtimebundle_test

import (
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
)

const testSecureKey32 = "01234567890123456789012345678901"

func testRuntimeBundlePlugins() config.PluginsConfig {
	return config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
	}
}

func TestBuild_secureSessionDisabled_leavesExecutorAndBuiltWithoutSS(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:       config.RoutingConfig{MaxAttempts: 3},
		Plugins:       testRuntimeBundlePlugins(),
		Continuity:    config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{Enabled: false},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := b.Executor
	if ex.SecureSessionEnabled || ex.SecureSession != nil || ex.SecureSessionRecorder != nil || ex.SessionDenialMapper != nil {
		t.Fatalf("expected no secure-session wiring when disabled, got enabled=%v mgr=%v rec=%v mapper_set=%v",
			ex.SecureSessionEnabled, ex.SecureSession, ex.SecureSessionRecorder, ex.SessionDenialMapper != nil)
	}
	if b.SecureSessionStore != nil {
		t.Fatalf("expected Built.SecureSessionStore nil, got %T", b.SecureSessionStore)
	}
}

func TestBuild_secureSessionMemory_wiresManagerDenialMapperAndStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Enabled:             true,
			Store:               "memory",
			TokenFingerprintKey: testSecureKey32,
		},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := b.Executor
	if !ex.SecureSessionEnabled || ex.SecureSession == nil {
		t.Fatal("expected secure session enabled with non-nil manager")
	}
	if ex.SecureSessionRecorder == nil {
		t.Fatal("expected recorder wired for memory store (non-durable recording)")
	}
	if ex.SecureSessionRecordingMandatory {
		t.Fatal("expected recording not mandatory for memory when audit_durability is not durable")
	}
	if ex.SessionDenialMapper == nil {
		t.Fatal("expected SessionDenialMapper")
	}
	if b.SecureSessionStore == nil {
		t.Fatal("expected Built.SecureSessionStore for diagnostics when secure session enabled")
	}
}

func TestBuild_secureSessionSQLite_wiresRecorderAndMandatoryWhenAuditDurable(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "ss.db")
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Enabled:             true,
			Store:               "sqlite",
			SQLitePath:          dbPath,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "durable",
		},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := b.Executor
	if ex.SecureSessionRecorder == nil {
		t.Fatal("expected full recorder for sqlite")
	}
	if !ex.SecureSessionRecordingMandatory {
		t.Fatal("expected recording mandatory when audit_durability is durable")
	}
	if b.SecureSessionStore == nil {
		t.Fatal("expected Built.SecureSessionStore")
	}
	// Closer: run all closers — sqlite should close without error
	for i, c := range b.Closers {
		if c == nil {
			t.Fatalf("closer %d nil", i)
		}
		if err := c(); err != nil {
			t.Fatalf("closer %d: %v", i, err)
		}
	}
}
