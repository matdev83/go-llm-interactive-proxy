package runtimebundle

import (
	"context"
	"fmt"
	"os"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
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

// registrySatisfiesEnabledBackendProfiles reports whether every enabled
// configured backend factory is already registered with a security profile.
// Used to treat a preloaded/custom registry as authoritative and skip artifact
// discovery (avoids unresolved-kind failures and discovered factory collisions).
func registrySatisfiesEnabledBackendProfiles(cfg *config.Config, reg *pluginreg.Registry) bool {
	if cfg == nil || reg == nil {
		return false
	}
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		if _, ok := reg.BackendSecurityProfile(p.FactoryID()); !ok {
			return false
		}
	}
	return true
}

// discoveredBackendInstall is the ownership bundle returned by
// installDiscoveredBackendExports. Host/staging transfer to ProcessServices or
// InspectPrepared; Artifacts must be closed before staging removal (Windows
// holds staged executable handles open until VerifiedArtifact.Close).
type discoveredBackendInstall struct {
	Host       *processhost.Host
	StagingDir string
	Artifacts  []*trust.VerifiedArtifact
}

func (d *discoveredBackendInstall) release() {
	if d == nil {
		return
	}
	for _, a := range d.Artifacts {
		_ = a.Close()
	}
	d.Artifacts = nil
	if d.Host != nil {
		_ = d.Host.Close()
		d.Host = nil
	}
	if d.StagingDir != "" {
		_ = os.RemoveAll(d.StagingDir)
		d.StagingDir = ""
	}
}

// installDiscoveredBackendExports discovers/trusts/catalogs optional backend
// exports and registers them on reg before ProcessServices freezes discovery.
//
// Policy (shared by BuildHost, ValidateDistribution, PrepareInspect):
//   - runs only when backend_discovery.enabled;
//   - if every enabled configured kind already has a security profile on reg,
//     treats the registry as authoritative and skips artifact discovery;
//   - otherwise requires configured external kinds to resolve fail-closed and
//     installs them.
//
// Caller owns the returned install until transferred to ProcessServices or
// InspectPrepared.Close.
func installDiscoveredBackendExports(cfg *config.Config, reg *pluginreg.Registry) (*discoveredBackendInstall, error) {
	if cfg == nil || !cfg.Plugins.BackendDiscovery.Enabled {
		return nil, nil
	}
	if registrySatisfiesEnabledBackendProfiles(cfg, reg) {
		return nil, nil
	}
	disc, staging, err := prepareDiscoveredPluginInstall(cfg, reg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: backend discovery: %w", err)
	}
	if disc == nil {
		return nil, nil
	}
	if err := InstallDiscoveredExports(reg, disc.Host, disc.Exports, disc.Options); err != nil {
		if disc.Host != nil {
			_ = disc.Host.Close()
		}
		if staging != "" {
			_ = os.RemoveAll(staging)
		}
		return nil, fmt.Errorf("runtimebundle: discovered plugin install: %w", err)
	}
	arts := make([]*trust.VerifiedArtifact, 0, len(disc.Exports))
	for _, exp := range disc.Exports {
		if exp.Artifact != nil {
			arts = append(arts, exp.Artifact)
		}
	}
	return &discoveredBackendInstall{
		Host:       disc.Host,
		StagingDir: staging,
		Artifacts:  arts,
	}, nil
}

func initProcessTracing(ctx context.Context, cfg *config.Config) (tracing.Result, error) {
	return tracing.Init(ctx, cfg)
}
