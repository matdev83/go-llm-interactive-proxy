package runtimebundle

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

// bootstrapEffectiveLoader is the singular startup effective-load operation.
// Callers pass it per invocation; there is no package-global loader hook.
type bootstrapEffectiveLoader func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error)

// LoadBootstrapEffectiveWithSource is the sole canonical startup effective-load
// owner (req: single config-load owner). It loads the fixed startup path
// through the shared strict effective-configuration pipeline used by
// BuildHost, ValidateDistribution, InspectRoutes/InspectInventory, and
// pkg/lipruntime.Build, plus the active source identity and the resolved
// CLI+environment stream-recovery override snapshot. The snapshot is captured
// exactly once for process composition and must be reused for every future
// effective reload (startup-fixed; no env reread).
//
// Order: stable source read → strict decode → defaults → fixed CLI/env
// stream-recovery overrides → standard feature injection → core validation →
// alias/prefix validation → private/public identity.
func LoadBootstrapEffectiveWithSource(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, config.StreamRecoveryOverrides, error) {
	if ctx == nil {
		return nil, nil, config.StreamRecoveryOverrides{}, fmt.Errorf("runtimebundle: nil context")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, config.StreamRecoveryOverrides{}, fmt.Errorf("runtimebundle: empty config path")
	}
	envOverrides, err := config.StreamRecoveryOverridesFromEnv()
	if err != nil {
		return nil, nil, config.StreamRecoveryOverrides{}, err
	}
	merged := mergeStreamRecoveryOverrides(envOverrides, cliOverrides)
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		return nil, nil, config.StreamRecoveryOverrides{}, err
	}
	snap, _, err := src.ReadStable(ctx, nil)
	if err != nil {
		return nil, nil, config.StreamRecoveryOverrides{}, err
	}
	eff, err := config.LoadEffective(ctx, snap.Bytes, config.LoadEffectiveOptions{
		ConfigDir:           filepath.Dir(src.AbsolutePath()),
		FixedStreamRecovery: &merged,
		InjectFeatures:      injectStandardBootstrapFeatures,
		ExtraValidate:       extraBootstrapValidate,
	})
	if err != nil {
		return nil, nil, config.StreamRecoveryOverrides{}, err
	}
	active := &configsource.ActiveSourceVersion{
		HandleIdentity: snap.HandleIdentity,
		PrivateDigest:  snap.PrivateDigest,
	}
	return eff, active, merged, nil
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
