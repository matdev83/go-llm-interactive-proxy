package runtimebundle

import (
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// closeObservableStore wraps fakeRetentionStore with an observable Close so leak
// tests can assert the control-plane store handle is released on Build failure.
// It satisfies controlplane.Store via the promoted fakeRetentionStore methods
// and interface{ Close() error } via Close, so buildControlPlaneStore returns
// Close as the closer when this is injected through BuildOptions.
type closeObservableStore struct {
	*fakeRetentionStore
	closed atomic.Bool
}

func (s *closeObservableStore) Close() error {
	s.closed.Store(true)
	return nil
}

// TestBuild_ControlPlaneCloserDisposedOnEarlyFailure proves the control-plane
// store closer is registered in Build's closer list immediately after the store
// opens and is disposed when a later startup step fails. Previously the closer
// was appended only after buildSecureSessionRuntime, so any failure between
// buildControlPlaneRuntime and that point leaked the sqlite/postgres handle.
//
// Not parallel: uses a BuildOptions injection (no package-level state is mutated,
// but the test is self-contained and need not run alongside parallel Build tests).
func TestBuild_ControlPlaneCloserDisposedOnEarlyFailure(t *testing.T) { //nolint:paralleltest // self-contained: documented Not parallel
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		},
		Auth: config.AuthConfig{EventDelivery: "bogus"}, // forces buildAuthEventDispatcher to fail after the store opens
	}
	_, err := Build(cfg, hooks.New(hooks.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)), &BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Testing: TestingOptions{
			ControlPlaneStoreOverride: store,
		},
	})
	if err == nil {
		t.Fatal("expected Build to fail on invalid auth.event_delivery")
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when Build fails after opening the store")
	}
}

// TestBuild_ControlPlaneCloserDisposedOnStreamRecoveryFailure covers a later
// error path: by the time streamRecoveryConfigFromConfig runs, closers already
// holds the control-plane store plus model-registry, continuity, and secure-
// session handles. Previously this path returned without disposing closers,
// leaking all of them.
func TestBuild_ControlPlaneCloserDisposedOnStreamRecoveryFailure(t *testing.T) { //nolint:paralleltest // self-contained: documented Not parallel
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		},
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{IdleTimeout: "not-a-duration"},
		},
	}
	_, err := Build(cfg, hooks.New(hooks.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)), &BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Testing: TestingOptions{
			ControlPlaneStoreOverride: store,
		},
	})
	if err == nil {
		t.Fatal("expected Build to fail on invalid stream_recovery idle_timeout")
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when Build fails at stream recovery config")
	}
}

// TestBuild_ControlPlaneCloserDisposedOnPricingFailure covers the latest Build
// failure point: accounting.NewPriceCatalog runs after the token-accounting
// closer is registered, so closers holds the control-plane store plus model,
// continuity, secure-session, and token-accounting handles. Locks the disposal
// invariant for the pricing error path before the Phase 2 build-unit extraction
// moves it into buildExecutorRuntime.
func TestBuild_ControlPlaneCloserDisposedOnPricingFailure(t *testing.T) { //nolint:paralleltest // self-contained: documented Not parallel
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		},
		Accounting: config.AccountingConfig{
			Pricing: config.AccountingPricingConfig{
				Models: []config.AccountingModelPriceConfig{{Model: "x"}}, // empty Backend -> NewPriceCatalog fails
			},
		},
	}
	_, err := Build(cfg, hooks.New(hooks.Config{}), slog.New(slog.NewTextHandler(io.Discard, nil)), &BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Testing: TestingOptions{
			ControlPlaneStoreOverride: store,
		},
	})
	if err == nil {
		t.Fatal("expected Build to fail on accounting pricing with empty model backend")
	}
	if !strings.Contains(err.Error(), "runtimebundle: accounting pricing") {
		t.Fatalf("expected pricing error, got %v", err)
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when Build fails at accounting pricing (latest step)")
	}
}
