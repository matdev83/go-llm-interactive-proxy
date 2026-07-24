package runtimebundle

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/tracing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// installRegistryAndRegistrations is the sole production InstallStandardBundleOn
// owner shared by BuildHost, ValidateDistribution, and Inspect entrypoints.
func installRegistryAndRegistrations(cfg *config.Config, mandatory []lipsdk.Requirement) (*pluginreg.Registry, []lipsdk.Registration, error) {
	reg := pluginreg.NewRegistry()
	apiKeys := standardplugins.ResolveUpstreamAPIKeysFromEnv()
	if err := standardplugins.InstallStandardBundleOn(reg, apiKeys); err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: plugin registration: %w", err)
	}
	if len(mandatory) > 0 {
		if err := reg.ValidateBundledFactories(mandatory); err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: registry factory validation: %w", err)
		}
	}
	regs := config.RegistrationsFromConfig(cfg)
	if _, err := featuresg.EnabledRegistrations(regs); err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: secrets-guard composition: %w", err)
	}
	return reg, regs, nil
}

func shutdownTracing(ctx context.Context, shutdown func(context.Context) error) {
	if shutdown == nil {
		return
	}
	_ = shutdown(context.WithoutCancel(ctx))
}

func initProcessTracing(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
	return tracing.Init(ctx, cfg)
}
