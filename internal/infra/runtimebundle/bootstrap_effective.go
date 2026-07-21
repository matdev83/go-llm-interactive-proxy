package runtimebundle

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

// LoadBootstrapEffective loads the fixed startup path through the shared strict
// effective-configuration pipeline used by BuildBootstrap, check-config, routes,
// inventory, serve, and pkg/lipruntime.Build. It does not enable runtime reload.
//
// Order: stable source read → strict decode → defaults → fixed CLI/env
// stream-recovery overrides → standard feature injection → core validation →
// alias/prefix validation → private/public identity.
func LoadBootstrapEffective(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides) (*config.EffectiveConfig, error) {
	if ctx == nil {
		return nil, fmt.Errorf("runtimebundle: nil context")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("runtimebundle: empty config path")
	}
	envOverrides, err := config.StreamRecoveryOverridesFromEnv()
	if err != nil {
		return nil, err
	}
	merged := mergeStreamRecoveryOverrides(envOverrides, cliOverrides)
	eff, _, err := configsource.LoadEffectiveFromPath(ctx, path, nil, config.LoadEffectiveOptions{
		FixedStreamRecovery: &merged,
		InjectFeatures:      injectStandardBootstrapFeatures,
		ExtraValidate:       extraBootstrapValidate,
	})
	if err != nil {
		return nil, err
	}
	return eff, nil
}

func injectStandardBootstrapFeatures(cfg *config.Config) error {
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{
		StandardDistribution: true,
	}); err != nil {
		return fmt.Errorf("tool-call-repair defaults: %w", err)
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{
		StandardDistribution: true,
	}); err != nil {
		return fmt.Errorf("reasoning-output-preservation defaults: %w", err)
	}
	return nil
}

func extraBootstrapValidate(cfg *config.Config) error {
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		return err
	}
	if err := standardplugins.ValidateCustomCompatibleBackendPrefixes(cfg.Plugins.Backends); err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	return nil
}
