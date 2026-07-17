package runtimebundle

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	coresg "github.com/matdev83/go-llm-interactive-proxy/internal/core/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretaudit"
	featuresg "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/secretsguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type secretGuardRuntime struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventoryExtras
}

func buildSecretGuardRuntime(cfg *config.Config, log *slog.Logger, opts *BuildOptions, regs []lipsdk.Registration) (*secretGuardRuntime, error) {
	if opts == nil {
		return nil, nil
	}
	mode := accessmode.ModeSingleUser
	accessMode := "single_user"
	if cfg != nil {
		var err error
		mode, err = cfg.EffectiveAccessMode()
		if err != nil {
			return nil, err
		}
		accessMode = string(mode)
	}
	runtimeCfg, err := featuresg.ComposeRuntimeConfig(accessMode, regs)
	if err != nil {
		return nil, err
	}
	inputs := opts.Extensions.SecretGuardInputs
	featureEnabled := runtimeCfg.Enabled
	singleUser := composeSecretGuardSingleUser(runtimeCfg, inputs)
	src, err := coresg.ComposeSource(mode, featureEnabled, opts.Extensions.SecretGuardEnvironment, singleUser)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: secret guard source: %w", err)
	}
	// Snapshot owns MaterializeSorted; composition only freezes a defensive clone.
	guards := slices.Clone(opts.Extensions.SecretGuards)
	accessPolicy := secretguard.AuditFailClosed
	if runtimeCfg.Enabled {
		accessPolicy = secretguard.AuditFailurePolicy(runtimeCfg.AuditFailurePolicy)
	}
	observer := opts.Extensions.SecretDecisionObserver
	if featureEnabled || len(guards) > 0 {
		if !secretguard.IsNilObserver(observer) {
			observer = secretguard.ChainObservers(accessPolicy, observer)
		} else {
			if log == nil {
				return nil, fmt.Errorf("runtimebundle: secrets-guard audit requires a non-nil logger")
			}
			slogObs, err := secretaudit.NewSlogObserver(log)
			if err != nil {
				return nil, fmt.Errorf("runtimebundle: secret guard audit: %w", err)
			}
			observer = secretguard.ChainObservers(accessPolicy, slogObs)
		}
	}
	var inventory *diag.InventoryExtras
	if featureEnabled || len(guards) > 0 {
		inventory = &diag.InventoryExtras{
			SecretGuardCatalogEntryCount: src.EntryCount(),
			SecretGuardSourceCategories:  append([]string(nil), src.SourceCategories()...),
			SecretGuardAccessMode:        accessMode,
			SecretGuardAction:            runtimeCfg.Action,
		}
	}
	return &secretGuardRuntime{
		Plane: extensions.SecretGuardPlane{
			Guards:             guards,
			MatcherResolver:    src.MatcherResolver(),
			DecisionObserver:   observer,
			AuditFailurePolicy: accessPolicy,
			AccessMode:         accessMode,
			ConfigVersion:      runtimeCfg.AuditConfigVersion,
		},
		Inventory: inventory,
	}, nil
}

// composeSecretGuardSingleUser merges YAML runtime config onto composition-seam inputs.
// YAML wins for catalog fields when the feature is enabled. Matcher options from
// inputs.SingleUser win when MatcherConfigured is already set (test/composition override);
// otherwise YAML stamps matcher options.
func composeSecretGuardSingleUser(runtimeCfg featuresg.RuntimeConfig, inputs SecretGuardInputs) coresg.SingleUserOptions {
	out := inputs.SingleUser
	out.IncludeEnv = append([]string(nil), out.IncludeEnv...)
	out.ExcludeEnv = append([]string(nil), out.ExcludeEnv...)
	if !runtimeCfg.Enabled {
		return out
	}
	matcherOverride := out.MatcherConfigured
	matcher := out.Matcher
	out.IncludePopularEnv = runtimeCfg.IncludePopularEnv
	out.IncludeEnv = append([]string(nil), runtimeCfg.IncludeEnv...)
	out.ExcludeEnv = append([]string(nil), runtimeCfg.ExcludeEnv...)
	out.MinSecretBytes = runtimeCfg.MinSecretBytes
	if matcherOverride {
		out.Matcher = matcher
		out.MatcherConfigured = true
		return out
	}
	out.Matcher = coresg.MatcherOptions{
		PreserveKnownPrefixes: runtimeCfg.PreserveKnownPrefixes,
		MaskByte:              runtimeCfg.MaskByte,
	}
	out.MatcherConfigured = true
	return out
}
