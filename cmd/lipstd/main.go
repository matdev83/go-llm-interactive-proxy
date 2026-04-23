package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/logging"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "./config/config.yaml", "path to runtime config")
	flag.Parse()

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap failed: %v\n", err)
		os.Exit(1)
	}

	traceRes, err := tracing.Init(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tracing init failed: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := traceRes.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "tracing shutdown: %v\n", err)
		}
	}()

	logger, err := logging.NewLogger(cfg.Logging, os.Stdout,
		logging.WithOTELTraceAttrs(cfg.Observability.Tracing.Enabled))
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger init failed: %v\n", err)
		os.Exit(1)
	}

	reg := pluginreg.NewRegistry()
	apiKeys := pluginreg.ResolveUpstreamAPIKeysFromEnv()
	if err := pluginreg.InstallStandardBundleOn(reg, apiKeys); err != nil {
		logger.Error("plugin registration failed", "error", err)
		os.Exit(1)
	}
	if err := reg.ValidateBundledFactories(mandatoryStandardPlugins()); err != nil {
		logger.Error("registry factory validation failed", "error", err)
		os.Exit(1)
	}

	regs := config.RegistrationsFromConfig(cfg)
	merged, err := reg.MergeFeatureSurface(regs)
	if err != nil {
		logger.Error("hook composition failed", "error", err)
		os.Exit(1)
	}
	merged.Hooks.ToolReactorErrorPolicy = config.ParseToolReactorErrorPolicy(cfg.Hooks.ToolReactorErrorPolicy)

	app, err := runtimebundle.NewBootstrapApp(runtimebundle.BootstrapOptions{
		Config:        cfg,
		Logger:        logger,
		Registrations: regs,
		Mandatory:     mandatoryStandardPlugins(),
		Hooks:         merged.Hooks,
		Lifecycles:    merged.Lifecycles,
	})
	if err != nil {
		logger.Error("runtime wiring failed", "error", err)
		os.Exit(1)
	}

	built, err := runtimebundle.Build(cfg, app.HookBus(), logger, &runtimebundle.BuildOptions{
		PluginRegistry:     reg,
		OutboundTracing:    traceRes.Active,
		SessionOpeners:     merged.SessionOpeners,
		WorkspaceResolvers: merged.WorkspaceResolvers,
		ToolCatalogFilters: merged.ToolCatalogFilters,
		RequestTransforms:  merged.RequestTransforms,
		RouteHintProviders: merged.RouteHintProviders,
		CompletionGates:    merged.CompletionGates,
		TrafficObservers:   merged.TrafficObservers,
		RawCaptureSinks:    merged.RawCaptureSinks,
		TrafficRedactors:   merged.TrafficRedactors,
	})
	if err != nil {
		logger.Error("runtime assembly failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := stdhttp.RunWithRuntime(ctx, cfg, app, logger, built); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
