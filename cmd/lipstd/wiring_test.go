package main

import (
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestReferenceConfigSatisfiesMandatoryStandardPlugins(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("model_aliases: %v", err)
	}

	regs := config.RegistrationsFromConfig(cfg)
	if err := lipsdk.ValidateRegistrations(regs, mandatoryStandardPlugins()); err != nil {
		t.Fatalf("bootstrap validation: %v", err)
	}
}
