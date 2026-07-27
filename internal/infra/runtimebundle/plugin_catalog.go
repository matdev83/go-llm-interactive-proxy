package runtimebundle

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/diagnostics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/discovery"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
)

// PluginCatalogResolution is the serve/inspect shared catalog snapshot plus
// installable exports derived with the same conflict policy.
type PluginCatalogResolution struct {
	Discovered  []discovery.Descriptor
	TrustBySafe map[string]trust.VerifyResult
	Snapshot    catalog.Snapshot
	CatalogErr  error
	Installable []ValidatedExport
}

// ResolvePluginCatalog runs the shared discover→trust→catalog path used by
// inspect and by standard serve bootstrap before factory install.
func ResolvePluginCatalog(cfg *config.Config, reg *pluginreg.Registry, stagingDir string) (PluginCatalogResolution, error) {
	if cfg == nil {
		return PluginCatalogResolution{}, fmt.Errorf("runtimebundle: ResolvePluginCatalog: nil config")
	}
	bd := cfg.Plugins.BackendDiscovery
	if stagingDir == "" {
		stagingDir = filepath.Join(os.TempDir(), "go-lip-plugin-catalog")
		_ = os.MkdirAll(stagingDir, 0o700)
	}
	res, err := diagnostics.ResolveCatalog(diagnostics.InspectInput{
		DiscoveryEnabled: bd.Enabled,
		Discovery:        diagnostics.DiscoveryFromConfig(bd),
		BuiltinKinds:     collectBuiltinKinds(reg),
		Configured:       configuredBackends(cfg),
		Strict:           bd.Strict,
		StagingDir:       stagingDir,
	})
	if err != nil {
		return PluginCatalogResolution{}, err
	}
	out := PluginCatalogResolution{
		Discovered:  res.Discovered,
		TrustBySafe: res.TrustBySafe,
		Snapshot:    res.Snapshot,
		CatalogErr:  res.CatalogErr,
	}
	if res.CatalogErr != nil {
		return out, nil
	}
	exports, err := CollectInstallableExports(res.Snapshot, res.TrustBySafe, res.Discovered)
	if err != nil {
		return PluginCatalogResolution{}, err
	}
	out.Installable = exports
	return out, nil
}

// prepareDiscoveredPluginInstall builds production host + installable exports
// for Build when typed backend discovery is enabled. Returns nil install when
// discovery is disabled. Caller owns host/staging cleanup via returned closer
// (register before plugin instance cleanups so dispose order is reverse).
func prepareDiscoveredPluginInstall(cfg *config.Config, reg *pluginreg.Registry) (*DiscoveredPluginInstall, string, error) {
	if cfg == nil || !cfg.Plugins.BackendDiscovery.Enabled {
		return nil, "", nil
	}
	staging, err := os.MkdirTemp("", "go-lip-plugin-serve-*")
	if err != nil {
		return nil, "", fmt.Errorf("runtimebundle: plugin staging: %w", err)
	}
	resolved, err := ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, "", err
	}
	if resolved.CatalogErr != nil {
		_ = os.RemoveAll(staging)
		return nil, "", resolved.CatalogErr
	}
	host := processhost.NewHost(processhost.Config{})
	return &DiscoveredPluginInstall{
		Host:    host,
		Exports: resolved.Installable,
		Options: DiscoveredInstallOptions{},
	}, staging, nil
}
