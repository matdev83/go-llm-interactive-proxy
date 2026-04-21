package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "./config/config.yaml", "path to runtime config")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		logger.Error("bootstrap failed", "error", err)
		os.Exit(1)
	}

	pluginreg.RegisterStandardBundle()
	reg := pluginreg.Default
	if err := reg.ValidateBundledFactories(mandatoryStandardPlugins()); err != nil {
		logger.Error("registry factory validation failed", "error", err)
		os.Exit(1)
	}

	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, lifes, err := reg.BuildFeatureHooks(regs)
	if err != nil {
		logger.Error("hook composition failed", "error", err)
		os.Exit(1)
	}
	hookCfg.ToolReactorErrorPolicy = config.ParseToolReactorErrorPolicy(cfg.Hooks.ToolReactorErrorPolicy)

	app, err := runtime.New(runtime.Options{
		Config:        cfg,
		Logger:        logger,
		Registrations: regs,
		Mandatory:     mandatoryStandardPlugins(),
		Hooks:         hookCfg,
		Lifecycles:    lifes,
	})
	if err != nil {
		logger.Error("runtime wiring failed", "error", err)
		os.Exit(1)
	}

	built, err := runtimebundle.Build(cfg, app.HookBus(), logger, &runtimebundle.BuildOptions{PluginRegistry: reg})
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
