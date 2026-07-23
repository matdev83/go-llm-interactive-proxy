package stdhttp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	coreconfig "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
)

// orderProbeLifecycle records Start/Stop order relative to closer callbacks.
type orderProbeLifecycle struct {
	started atomic.Bool
	stopped atomic.Bool
	events  *[]string
}

func (o *orderProbeLifecycle) Start(context.Context) error {
	o.started.Store(true)
	*o.events = append(*o.events, "app.start")
	return nil
}

func (o *orderProbeLifecycle) Stop(context.Context) error {
	o.stopped.Store(true)
	*o.events = append(*o.events, "app.shutdown")
	return nil
}

func TestNewStandardHandler_cleanupOrderAndIdempotentClosers(t *testing.T) {
	t.Parallel()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Frontends: []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	log := testkit.DiscardLogger()
	var events []string
	life := &orderProbeLifecycle{events: &events}
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Logger:     log,
		Lifecycles: []lipplugin.Lifecycle{life},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	var closerRuns int32
	built.Closers = append(built.Closers, func() error {
		atomic.AddInt32(&closerRuns, 1)
		events = append(events, "closer")
		return nil
	})

	h, cleanup, err := NewStandardHandler(context.Background(), cfg, app, log, built)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("nil handler")
	}
	if !life.started.Load() {
		t.Fatal("app.Start must run after successful mount composition")
	}

	cleanup(context.Background())
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1 after first cleanup", closerRuns)
	}
	if !life.stopped.Load() {
		t.Fatal("cleanup must shut down app")
	}
	shutdownIdx, closerIdx := -1, -1
	for i, e := range events {
		switch e {
		case "app.shutdown":
			if shutdownIdx < 0 {
				shutdownIdx = i
			}
		case "closer":
			closerIdx = i
		}
	}
	if shutdownIdx < 0 || closerIdx < 0 || shutdownIdx > closerIdx {
		t.Fatalf("cleanup order events=%v want app.shutdown before closer", events)
	}
	cleanup(context.Background()) // repeated cleanup must not re-run closers
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1 (idempotent)", closerRuns)
	}
}

func TestNewStandardHandler_mountFailureReleasesClosersWithoutStart(t *testing.T) {
	t.Parallel()
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
	log := testkit.DiscardLogger()
	var events []string
	life := &orderProbeLifecycle{events: &events}
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Logger:     log,
		Lifecycles: []lipplugin.Lifecycle{life},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	var closerRuns int32
	built.Closers = append(built.Closers, func() error {
		atomic.AddInt32(&closerRuns, 1)
		return nil
	})
	_, _, err = NewStandardHandler(context.Background(), cfg, app, log, built)
	if err == nil {
		t.Fatal("expected mount failure")
	}
	if life.started.Load() {
		t.Fatal("app.Start must not run when mounts fail")
	}
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1 on mount failure", closerRuns)
	}
}

func TestNewStandardHandler_appStartFailureShutsDownAndReleasesClosers(t *testing.T) {
	t.Parallel()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Frontends: []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Logger:     log,
		Lifecycles: []lipplugin.Lifecycle{failStartLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	var closerRuns int32
	built.Closers = append(built.Closers, func() error {
		atomic.AddInt32(&closerRuns, 1)
		return nil
	})
	_, _, err = NewStandardHandler(context.Background(), cfg, app, log, built)
	if err == nil {
		t.Fatal("expected start failure")
	}
	if !errors.Is(err, errTestStartFail) {
		t.Fatalf("want start fail cause, got %v", err)
	}
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1 on start failure", closerRuns)
	}
}

func TestComposeRequestPlane_projectsWithoutBuiltRehydration(t *testing.T) {
	t.Parallel()
	httpInput := mustReadFile(t, "http_input.go")
	if !strings.Contains(httpInput, "func standardHTTPInputFromRequestPlane") {
		t.Fatal("missing standardHTTPInputFromRequestPlane")
	}
	rp := mustReadFile(t, "request_plane.go")
	if strings.Contains(rp, "requestPlaneAsBuilt") {
		t.Fatal("requestPlaneAsBuilt must remain deleted")
	}
}
