package cursorsdk

import (
	"context"
	"errors"
	"fmt"
)

var ErrSandboxUnavailable = errors.New("cursor_sdk_sandbox_unavailable")

func validateSettingSources(sources []SettingSource) error {
	for _, s := range sources {
		if !validSettingSource(s) {
			if s == "" {
				return errors.New("cursorsdk: setting_sources entries must be non-empty")
			}
			return fmt.Errorf("cursorsdk: unknown setting_sources value %q (allowed: project, user, team, mdm, plugins, all per @cursor/sdk@1.0.23)", s)
		}
	}
	return nil
}

func (rt *backendRuntime) enforceSecurityPolicy(ctx context.Context) error {
	if err := validateSettingSources(rt.cfg.SettingSources); err != nil {
		return err
	}
	if _, err := NormalizeMCPServers(rt.cfg.MCPServers); err != nil {
		return err
	}
	return rt.enforceSandboxPolicy(ctx)
}

func (rt *backendRuntime) enforceSandboxPolicy(ctx context.Context) error {
	if EffectiveSandboxMode(rt.cfg.SandboxMode) != SandboxRequired {
		return nil
	}
	info, err := rt.agent.EnsureReady(ctx)
	if err != nil {
		return err
	}
	if !info.SandboxSupported {
		return fmt.Errorf("%w: sandbox required but unavailable on this host/SDK (set sandbox_mode: off for local-only)", ErrSandboxUnavailable)
	}
	return nil
}
