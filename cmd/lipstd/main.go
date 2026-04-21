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

	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, err := featureHooksFromRegistrations(regs)
	if err != nil {
		logger.Error("hook composition failed", "error", err)
		os.Exit(1)
	}

	app, err := runtime.New(runtime.Options{
		Config:        cfg,
		Logger:        logger,
		Registrations: regs,
		Mandatory:     mandatoryStandardPlugins(),
		Hooks:         hookCfg,
	})
	if err != nil {
		logger.Error("runtime wiring failed", "error", err)
		os.Exit(1)
	}

	if err := app.Start(context.Background()); err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := stdhttp.Run(ctx, cfg, app.HookBus(), logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
