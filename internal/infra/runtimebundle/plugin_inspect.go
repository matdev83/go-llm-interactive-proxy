package runtimebundle

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/catalog"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/diagnostics"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/discovery"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
)

// PluginInspectReport is the operator-facing inspect snapshot.
type PluginInspectReport = diagnostics.Report

// PluginDoctorReport is the operator-facing doctor snapshot.
type PluginDoctorReport = diagnostics.DoctorReport

// InspectBackendPlugins runs non-executing discovery/catalog inspect for cfg.
// It uses the same ResolvePluginCatalog path as standard serve bootstrap.
func InspectBackendPlugins(cfg *config.Config, reg *pluginreg.Registry) (PluginInspectReport, error) {
	return InspectBackendPluginsCtx(context.Background(), cfg, reg)
}

// InspectBackendPluginsCtx is like InspectBackendPlugins but accepts a context for live inventory projection.
func InspectBackendPluginsCtx(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry) (PluginInspectReport, error) {
	if cfg == nil {
		return PluginInspectReport{}, fmt.Errorf("runtimebundle: InspectBackendPlugins: nil config")
	}
	staging, err := os.MkdirTemp("", "go-lip-plugin-inspect-*")
	if err != nil {
		return PluginInspectReport{}, fmt.Errorf("runtimebundle: InspectBackendPlugins: staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	resolved, err := ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		return PluginInspectReport{}, err
	}
	compatibleRows := standardplugins.ProjectCompatibleBackendRows(cfg)
	if live, loadErr := tryLoadInventoryLiveSnapshot(ctx, cfg, reg); loadErr == nil {
		defer func() { _ = live.Close(ctx) }()
		compatibleRows = standardplugins.ProjectCompatibleBackendRowsLive(cfg, live.compatibleInputs())
	}
	rep := diagnostics.FormatInspectReportWithCompatible(diagnostics.CatalogResolution{
		Discovered:  resolved.Discovered,
		TrustBySafe: resolved.TrustBySafe,
		Snapshot:    resolved.Snapshot,
		CatalogErr:  resolved.CatalogErr,
	}, collectNativeBuiltinKinds(reg), standardplugins.CollectBuiltinCompatibleKinds(reg), configuredBackends(cfg)).WithCompatibleBackends(compatibleRows)
	return rep, resolved.CatalogErr
}

// DoctorBackendPlugin launches only the selected configured instance id.
func DoctorBackendPlugin(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, instanceID string, host *processhost.Host) (PluginDoctorReport, error) {
	if cfg == nil {
		return PluginDoctorReport{}, fmt.Errorf("runtimebundle: DoctorBackendPlugin: nil config")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return PluginDoctorReport{}, fmt.Errorf("runtimebundle: DoctorBackendPlugin: empty instance id")
	}
	if host == nil {
		host = processhost.NewHost(processhost.Config{})
	}

	for _, b := range cfg.Plugins.Backends {
		if b.InstanceID() != instanceID || !b.Enabled {
			continue
		}
		kind := b.FactoryID()
		if reg != nil && reg.HasBackend(kind) && !isExternalDiscoveryKind(kind, cfg.Plugins.BackendDiscovery, reg) {
			return PluginDoctorReport{Results: []diagnostics.DoctorResult{{
				InstanceID: instanceID,
				Kind:       kind,
				State:      catalog.StateConfigured,
				Reason:     "builtin",
				Guidance:   "built-in backends do not use process doctor; inspect reports registration state without launch",
			}}}, nil
		}
	}

	targets, err := resolveDoctorTargets(cfg, reg, []string{instanceID})
	if err != nil {
		return PluginDoctorReport{}, err
	}
	return diagnostics.Doctor(ctx, diagnostics.DoctorInput{
		InstanceIDs: []string{instanceID},
		Targets:     targets,
		Host:        host,
	})
}

func collectNativeBuiltinKinds(reg *pluginreg.Registry) []string {
	all := collectBuiltinKinds(reg)
	compatible := map[string]struct{}{}
	for _, k := range standardplugins.CollectBuiltinCompatibleKinds(reg) {
		compatible[k] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, k := range all {
		if _, ok := compatible[k]; ok {
			continue
		}
		out = append(out, k)
	}
	return out
}

func collectBuiltinKinds(reg *pluginreg.Registry) []string {
	// Prefer registry provenance so custom composition-root builtins are retained
	// and discovered exports installed later cannot self-classify as builtins.
	if reg != nil {
		if ids := reg.BuiltinBackendFactoryIDs(); len(ids) > 0 {
			return ids
		}
	}
	return append([]string(nil), standardplugins.EssentialBackendKinds...)
}

func configuredBackends(cfg *config.Config) []diagnostics.ConfiguredBackend {
	out := make([]diagnostics.ConfiguredBackend, 0, len(cfg.Plugins.Backends))
	for _, b := range cfg.Plugins.Backends {
		out = append(out, diagnostics.ConfiguredBackend{
			InstanceID: b.InstanceID(),
			Kind:       b.FactoryID(),
			Enabled:    b.Enabled,
		})
	}
	return out
}

func resolveDoctorTargets(cfg *config.Config, reg *pluginreg.Registry, instanceIDs []string) (map[string]diagnostics.DoctorTarget, error) {
	want := map[string]struct{}{}
	for _, id := range instanceIDs {
		want[strings.TrimSpace(id)] = struct{}{}
	}
	targets := map[string]diagnostics.DoctorTarget{}
	bd := cfg.Plugins.BackendDiscovery

	for _, b := range cfg.Plugins.Backends {
		id := b.InstanceID()
		if _, ok := want[id]; !ok || !b.Enabled {
			continue
		}
		kind := b.FactoryID()
		if reg != nil && reg.HasBackend(kind) && !isExternalDiscoveryKind(kind, bd, reg) {
			// Built-in configured backends are not process-doctor targets.
			targets[id] = diagnostics.DoctorTarget{InstanceID: id, Kind: kind}
			continue
		}
		art, err := resolveConfiguredArtifact(bd, kind)
		if err != nil {
			targets[id] = diagnostics.DoctorTarget{InstanceID: id, Kind: kind}
			continue
		}
		raw, _ := encodeOpaqueYAML(b.Config)
		targets[id] = diagnostics.DoctorTarget{
			InstanceID: id,
			Kind:       kind,
			Artifact:   art,
			ConfigYAML: raw,
			// Doctor never preloads connector credentials; channel check uses empty secrets.
			Secrets: backendplugin.SecretBundle{},
			Model:   processhost.ProcessModelPerInstance,
		}
	}
	return targets, nil
}

func isExternalDiscoveryKind(kind string, bd config.BackendDiscoveryConfig, reg *pluginreg.Registry) bool {
	if !bd.Enabled {
		return false
	}
	if reg != nil {
		return !slices.Contains(reg.BuiltinBackendFactoryIDs(), kind)
	}
	return !slices.Contains(standardplugins.EssentialBackendKinds, kind)
}

func resolveConfiguredArtifact(bd config.BackendDiscoveryConfig, kind string) (*trust.VerifiedArtifact, error) {
	if !bd.Enabled {
		return nil, fmt.Errorf("discovery disabled")
	}
	res, err := discovery.Discover(diagnostics.DiscoveryFromConfig(bd))
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp("", "go-lip-plugin-doctor-*")
	if err != nil {
		return nil, err
	}
	// Caller (doctor session) owns cleanup via processhost/staging lifecycle; keep dir for Verify bind.
	for _, d := range res.Descriptors {
		if d.Status != discovery.StatusDiscovered {
			continue
		}
		if !manifestExportsKind(d.Manifest, kind) {
			continue
		}
		tr := trust.Verify(d.Root, d.Manifest, trust.VerifyOptions{StagingDir: staging})
		if tr.Reason == trust.ReasonOK && tr.Artifact != nil {
			return tr.Artifact, nil
		}
	}
	_ = os.RemoveAll(staging)
	return nil, fmt.Errorf("no trusted artifact for kind %q", kind)
}

func manifestExportsKind(m sdkmanifest.Manifest, kind string) bool {
	for _, e := range m.Exports {
		if e.Kind == kind {
			return true
		}
	}
	return false
}
