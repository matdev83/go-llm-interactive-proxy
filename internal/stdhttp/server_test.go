package stdhttp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

type cancelSensitiveLifecycle struct{}

func (cancelSensitiveLifecycle) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (cancelSensitiveLifecycle) Stop(context.Context) error { return nil }

type failStartLifecycle struct{}

func (failStartLifecycle) Start(context.Context) error { return errors.New("fail start") }

func (failStartLifecycle) Stop(context.Context) error { return nil }

func TestRunWithRuntime_invokesClosersOnMountFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Frontends: []coreconfig.PluginConfig{
				{ID: "not-a-registered-frontend-plugin", Enabled: true},
			},
		},
	}
	app, err := runtime.New(runtime.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var closerRuns int32
	built.Closers = append(built.Closers, func() error {
		atomic.AddInt32(&closerRuns, 1)
		return nil
	})
	err = RunWithRuntime(ctx, cfg, app, nil, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1", closerRuns)
	}
}

func TestRunWithRuntime_invokesClosersOnAppStartFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Frontends: []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Lifecycles: []lipplugin.Lifecycle{failStartLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := runtimebundle.Build(cfg, app.HookBus(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var closerRuns int32
	built.Closers = append(built.Closers, func() error {
		atomic.AddInt32(&closerRuns, 1)
		return nil
	})
	err = RunWithRuntime(ctx, cfg, app, nil, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1", closerRuns)
	}
}

func TestRun_appStartReceivesRunContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &coreconfig.Config{
		Server: coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing: coreconfig.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "openai-responses:gpt-4o-mini",
		},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
	}
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Lifecycles: []lipplugin.Lifecycle{cancelSensitiveLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = Run(ctx, cfg, app, nil)
	if err == nil {
		t.Fatal("expected error when ctx is cancelled before startup (app.Start must observe Run's ctx)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
