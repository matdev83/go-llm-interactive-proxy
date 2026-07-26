package config

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LoadEffectiveOptions configures the shared effective-load pipeline used by
// startup, check-config, and runtime reload. Feature injection and extra
// validation are explicit seams so core/config does not import plugin packages.
type LoadEffectiveOptions struct {
	// ConfigDir sets Config.ConfigDir when non-empty (typically the directory of
	// the fixed source path).
	ConfigDir string
	// FixedStreamRecovery, when non-nil, materializes CLI/env stream-recovery
	// overrides into the effective config (even when the struct is zero).
	FixedStreamRecovery *StreamRecoveryOverrides
	// InjectFeatures is the standard-distribution feature injection seam.
	InjectFeatures func(*Config) error
	// ExtraValidate runs after core Validate (routing aliases, prefix checks, …).
	ExtraValidate func(*Config) error
}

// EffectiveConfig is the normalized effective candidate produced by LoadEffective.
// Identity.PrivateDigest is private; PublicFingerprint is secret-safe.
type EffectiveConfig struct {
	Config   *Config
	Identity EffectiveIdentity
	Category LoadCategory
	LoadedAt time.Time
}

// LoadEffective runs the deterministic effective pipeline:
// classify → strict one-document decode → defaults → fixed stream-recovery
// overrides → feature injection → core validation → extra validation →
// private/public identity.
func LoadEffective(ctx context.Context, raw []byte, opts LoadEffectiveOptions) (*EffectiveConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, cat, err := StrictDecode(raw)
	if err != nil {
		return nil, err
	}
	if opts.ConfigDir != "" {
		cfg.ConfigDir = opts.ConfigDir
	}
	applyLoadDefaults(cfg)

	if opts.FixedStreamRecovery != nil {
		if err := ApplyStreamRecoveryOverrides(cfg, *opts.FixedStreamRecovery); err != nil {
			return nil, err
		}
	}
	if opts.InjectFeatures != nil {
		if err := opts.InjectFeatures(cfg); err != nil {
			return nil, fmt.Errorf("config: feature injection: %w", err)
		}
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	if opts.ExtraValidate != nil {
		if err := opts.ExtraValidate(cfg); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := ComputeEffectiveIdentity(cfg)
	if err != nil {
		return nil, err
	}
	return &EffectiveConfig{
		Config:   cfg,
		Identity: id,
		Category: cat,
		LoadedAt: time.Now().UTC(),
	}, nil
}

// ApplyStreamRecoveryOverrides materializes effective auto-resume settings into cfg.
func ApplyStreamRecoveryOverrides(cfg *Config, overrides StreamRecoveryOverrides) error {
	if cfg == nil {
		return fmt.Errorf("config: nil config")
	}
	eff, err := EffectiveStreamRecoveryAutoResume(cfg, overrides)
	if err != nil {
		return err
	}
	enabled := eff.Enabled
	emit := eff.EmitWarning
	cfg.StreamRecovery.AutoResume.Enabled = &enabled
	cfg.StreamRecovery.AutoResume.IdleTimeout = eff.IdleTimeout.String()
	cfg.StreamRecovery.AutoResume.GracePeriod = eff.GracePeriod.String()
	cfg.StreamRecovery.AutoResume.PostOutputPolicy = string(eff.PostOutputPolicy)
	cfg.StreamRecovery.AutoResume.EmitWarning = &emit
	return nil
}

// applyLoadDefaults applies the same defaults historically owned by LoadFile.
func applyLoadDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	applyDefaultServerListenAddress(cfg)
	if cfg.Auth.LocalAPIKeys == nil {
		cfg.Auth.LocalAPIKeys = []AuthLocalAPIKeyRecord{}
	}
	if cfg.Diagnostics.HealthPath == "" {
		cfg.Diagnostics.HealthPath = "/healthz"
	}
	if cfg.Diagnostics.AttemptsPath == "" {
		cfg.Diagnostics.AttemptsPath = "/admin/attempts"
	}
	if cfg.Routing.MaxAttempts == 0 {
		cfg.Routing.MaxAttempts = 3
	}
	if cfg.Continuity.InMemory && strings.TrimSpace(cfg.Continuity.Store) == "" {
		cfg.Continuity.Store = "memory"
	}
	if cfg.SecureSessionEffectivelyEnabled() && strings.TrimSpace(cfg.SecureSession.Store) == "" {
		cfg.SecureSession.Store = "memory"
	}
	if strings.TrimSpace(cfg.Logging.Level) == "" {
		cfg.Logging.Level = "info"
	}
	if strings.TrimSpace(cfg.Logging.Format) == "" {
		cfg.Logging.Format = "json"
	}
	if mp := strings.TrimSpace(cfg.Observability.Metrics.Path); mp == "" {
		cfg.Observability.Metrics.Path = "/metrics"
	} else {
		cfg.Observability.Metrics.Path = mp
	}
	if cfg.ModelCatalog.ModelOverrides == nil {
		cfg.ModelCatalog.ModelOverrides = []ModelCatalogModelOverrideEntry{}
	}
	if cfg.ModelCatalog.BackendModelOverrides == nil {
		cfg.ModelCatalog.BackendModelOverrides = []ModelCatalogBackendModelOverrideEntry{}
	}
}
