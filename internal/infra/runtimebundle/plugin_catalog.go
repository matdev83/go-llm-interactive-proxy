package runtimebundle

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

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
		closeVerifiedArtifacts(res.TrustBySafe)
		return PluginCatalogResolution{}, err
	}
	out.Installable = exports
	return out, nil
}

// prepareDiscoveredPluginInstall builds production host + installable exports
// for Build when typed backend discovery is enabled. Returns nil install when
// discovery is disabled. The private pool is created beside the host and is
// transferred with the install ownership bundle before factory registration.
func prepareDiscoveredPluginInstall(cfg *config.Config, reg *pluginreg.Registry) (*DiscoveredPluginInstall, *backendResourcePool, string, error) {
	if cfg == nil || !cfg.Plugins.BackendDiscovery.Enabled {
		return nil, nil, "", nil
	}
	staging, err := os.MkdirTemp("", "go-lip-plugin-serve-*")
	if err != nil {
		return nil, nil, "", fmt.Errorf("runtimebundle: plugin staging: %w", err)
	}
	resolved, err := ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		_ = removeAllRetry(staging, 8, 25*time.Millisecond)
		return nil, nil, "", err
	}
	if resolved.CatalogErr != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		_ = removeAllRetry(staging, 8, 25*time.Millisecond)
		return nil, nil, "", resolved.CatalogErr
	}
	trusted := make([]*trust.VerifiedArtifact, 0, len(resolved.TrustBySafe))
	for _, tr := range resolved.TrustBySafe {
		if tr.Artifact != nil {
			trusted = append(trusted, tr.Artifact)
		}
	}
	host := processhost.NewHost(processhost.Config{})
	resourcePool := newBackendResourcePool()
	return &DiscoveredPluginInstall{
		Host:    host,
		Exports: resolved.Installable,
		Trusted: trusted,
		Options: DiscoveredInstallOptions{},
	}, resourcePool, staging, nil
}
