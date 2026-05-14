package runtimebundle

import "github.com/matdev83/go-llm-interactive-proxy/internal/core/config"

func mergeStreamRecoveryOverrides(env, cli config.StreamRecoveryOverrides) config.StreamRecoveryOverrides {
	out := env
	if cli.CLIEnabled != nil {
		out.CLIEnabled = cli.CLIEnabled
	}
	if cli.CLIIdleTimeout > 0 {
		out.CLIIdleTimeout = cli.CLIIdleTimeout
	}
	if cli.CLIGracePeriod > 0 {
		out.CLIGracePeriod = cli.CLIGracePeriod
	}
	if cli.CLIPostOutputPolicy != "" {
		out.CLIPostOutputPolicy = cli.CLIPostOutputPolicy
	}
	if cli.CLIEmitWarning != nil {
		out.CLIEmitWarning = cli.CLIEmitWarning
	}
	return out
}

func applyEffectiveStreamRecovery(cfg *config.Config, eff config.EffectiveAutoResumeConfig) {
	if cfg == nil {
		return
	}
	cfg.StreamRecovery.AutoResume.Enabled = &eff.Enabled
	cfg.StreamRecovery.AutoResume.IdleTimeout = eff.IdleTimeout.String()
	cfg.StreamRecovery.AutoResume.GracePeriod = eff.GracePeriod.String()
	cfg.StreamRecovery.AutoResume.PostOutputPolicy = string(eff.PostOutputPolicy)
	cfg.StreamRecovery.AutoResume.EmitWarning = &eff.EmitWarning
}
