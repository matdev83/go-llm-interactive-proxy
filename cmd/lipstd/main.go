package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
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

	app, err := runtime.New(runtime.Options{
		Config:        cfg,
		Logger:        logger,
		Registrations: config.RegistrationsFromConfig(cfg),
		Mandatory:     mandatoryStandardPlugins(),
	})
	if err != nil {
		logger.Error("runtime wiring failed", "error", err)
		os.Exit(1)
	}

	if err := app.Start(context.Background()); err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}

	logger.Info("bootstrap scaffold ready", "note", "runtime behavior is not implemented yet")
}
