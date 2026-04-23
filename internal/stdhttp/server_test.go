package stdhttp

import (
	"context"
	"errors"
	"net"
	"slices"
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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var errTestStartFail = errors.New("test start fail")

type cancelSensitiveLifecycle struct{}

func (cancelSensitiveLifecycle) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (cancelSensitiveLifecycle) Stop(context.Context) error { return nil }

type failStartLifecycle struct{}

func (failStartLifecycle) Start(context.Context) error { return errTestStartFail }

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
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: log})
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
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stdhttp: mount frontends") {
		t.Fatalf("error %q missing mount frontends context", err.Error())
	}
	inner := errors.Unwrap(err)
	if inner == nil || !strings.Contains(inner.Error(), "pluginreg: unknown frontend") {
		t.Fatalf("want pluginreg inner via unwrap, got inner=%v err=%v", inner, err)
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
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{
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
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stdhttp: start app") {
		t.Fatalf("error %q missing start app context", err.Error())
	}
	if !errors.Is(err, errTestStartFail) {
		t.Fatalf("errors.Is start cause: got %v", err)
	}
	if atomic.LoadInt32(&closerRuns) != 1 {
		t.Fatalf("closer runs=%d want 1", closerRuns)
	}
}

func TestRun_wrapsBuildNilConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
	}
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: testkit.DiscardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	err = Run(ctx, nil, app, testkit.DiscardLogger(), pluginreg.NewRegistry())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stdhttp: build runtime") {
		t.Fatalf("error %q missing build runtime context", err.Error())
	}
	inner := errors.Unwrap(err)
	if inner == nil || !strings.Contains(inner.Error(), "runtimebundle: nil config") {
		t.Fatalf("want runtimebundle inner, got inner=%v err=%v", inner, err)
	}
}

func TestRunWithRuntime_wrapsAttemptsNilStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: coreconfig.DiagnosticsConfig{
			Enabled:      true,
			AttemptsPath: "/attempts",
		},
	}
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	built.Store = nil
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stdhttp: attempts handler") {
		t.Fatalf("error %q missing attempts handler context", err.Error())
	}
	inner := errors.Unwrap(err)
	if inner == nil || !strings.Contains(inner.Error(), "diag: AttemptsHandler: nil store") {
		t.Fatalf("want diag inner, got inner=%v err=%v", inner, err)
	}
}

func TestRunWithRuntime_wrapsServeAddrInUse(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: addr},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3, DefaultRoute: "openai-responses:gpt-4o-mini"},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Frontends: []coreconfig.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stdhttp: serve") {
		t.Fatalf("error %q missing serve context", err.Error())
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected *net.OpError in chain, got %T %v", err, err)
	}
}

func TestRunWithRuntime_appStartReceivesRunContext(t *testing.T) {
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
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Logger:     log,
		Lifecycles: []lipplugin.Lifecycle{cancelSensitiveLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error when ctx is cancelled before startup (app.Start must observe RunWithRuntime ctx)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRunWithRuntime_metricsEnabledRequiresBuiltMetrics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Server:     coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3, DefaultRoute: "openai-responses:gpt-4o-mini"},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Observability: coreconfig.ObservabilityConfig{
			Metrics: coreconfig.MetricsConfig{Enabled: true, Path: "/metrics"},
		},
	}
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	built, err := runtimebundle.Build(cfg, app.HookBus(), log, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if err != nil {
		t.Fatal(err)
	}
	built.Metrics = nil
	err = RunWithRuntime(ctx, cfg, app, log, built)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "built.Metrics") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_initializesTracingAndOutboundPropagation(t *testing.T) { //nolint:paralleltest // mutates global OpenTelemetry providers
	originalProvider := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(originalProvider)
		otel.SetTextMapPropagator(originalPropagator)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &coreconfig.Config{
		Server: coreconfig.ServerConfig{Address: "127.0.0.1:0"},
		Routing: coreconfig.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "openai-responses:gpt-4o-mini",
		},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Observability: coreconfig.ObservabilityConfig{
			Tracing: coreconfig.TracingConfig{Enabled: true},
		},
	}
	log := testkit.DiscardLogger()
	app, err := runtime.New(runtime.Options{
		Config:     cfg,
		Logger:     log,
		Lifecycles: []lipplugin.Lifecycle{cancelSensitiveLifecycle{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	err = Run(ctx, cfg, app, log, reg)
	if err == nil {
		t.Fatal("expected error when ctx is cancelled before startup")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("expected tracing.Init to install sdk tracer provider, got %T", otel.GetTracerProvider())
	}
	fields := otel.GetTextMapPropagator().Fields()
	got := slices.Clone(fields)
	want := []string{"traceparent", "tracestate", "baggage"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("expected tracing.Init to install tracecontext+baggage propagator fields, got %v", fields)
	}
}
