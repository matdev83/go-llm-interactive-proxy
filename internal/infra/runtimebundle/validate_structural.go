package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"gopkg.in/yaml.v3"
)

// ValidateStructuralInput configures structural check-config validation.
// It performs manifest-available ownership and compatible-backend structural
// checks without runtime tracing, process services, generation compile, plugin
// activation, or provider network I/O.
type ValidateStructuralInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
}

type structuralValidationOps struct {
	load     bootstrapEffectiveLoader
	registry registryInstaller
}

func defaultStructuralValidationOps() structuralValidationOps {
	return structuralValidationOps{
		load:     LoadBootstrapEffectiveWithSource,
		registry: installRegistryOp,
	}
}

// ValidateStructural validates effective configuration for operator check-config.
// Requirements 2.8 and 5.5: structural rules only; no plugin process activation,
// runtime tracing, model-registry refresh, or provider network calls.
func ValidateStructural(ctx context.Context, in ValidateStructuralInput) error {
	return validateStructural(ctx, in, defaultStructuralValidationOps())
}

func validateStructural(ctx context.Context, in ValidateStructuralInput, ops structuralValidationOps) error {
	if ctx == nil {
		return fmt.Errorf("runtimebundle: nil context")
	}
	if ops.load == nil || ops.registry == nil {
		return fmt.Errorf("runtimebundle: incomplete structural validation operations")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return fmt.Errorf("runtimebundle: empty config path")
	}

	effective, _, _, err := ops.load(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return err
	}
	if effective == nil || effective.Config == nil {
		return fmt.Errorf("runtimebundle: nil effective config")
	}
	cfg := effective.Config

	reg, regs, err := ops.registry(cfg, in.Mandatory)
	if err != nil {
		return fmt.Errorf("runtimebundle: structural registry: %w", err)
	}

	if err := registerManifestDiscoveredKinds(cfg, reg); err != nil {
		return err
	}
	if err := validateCandidateManifestOwnership(cfg, reg); err != nil {
		return err
	}
	if err := validateRoutesStructure(cfg); err != nil {
		return fmt.Errorf("runtimebundle: structural routes: %w", err)
	}
	projection := inventoryProjectionForOperator(cfg, nil)
	if _, err := inventorySnapshotForOperatorWithProjection(ctx, cfg, reg, regs, projection); err != nil {
		return fmt.Errorf("runtimebundle: structural inventory: %w", err)
	}
	if err := validateProjectedDiagnostics(projection.InstanceDiagnostics); err != nil {
		return err
	}
	return nil
}

func validateProjectedDiagnostics(rows []diag.InstanceDiagnostic) error {
	for _, row := range rows {
		if strings.TrimSpace(row.ConfigError) == "" {
			continue
		}
		inst := strings.TrimSpace(row.InstanceID)
		if inst == "" {
			inst = strings.TrimSpace(row.ID)
		}
		if inst == "" {
			inst = "<unknown>"
		}
		kind := strings.TrimSpace(row.FactoryKind)
		if kind != "" && inst != kind && inst != "<unknown>" {
			return fmt.Errorf("runtimebundle: extension %q (%s): %s", inst, kind, row.ConfigError)
		}
		return fmt.Errorf("runtimebundle: extension %q: %s", inst, row.ConfigError)
	}
	return nil
}

func validateRoutesStructure(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	raw := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	if _, err := routing.NewAliasResolver(routing.ModelAliasRulesFromConfig(cfg)); err != nil {
		return fmt.Errorf("model_aliases: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("empty effective default route")
	}
	return nil
}

// registerManifestDiscoveredKinds resolves trusted plugin manifests from disk
// and registers factory kinds on reg for manifest-available ownership checks.
// It never creates a process host, activates plugins, or dials providers.
func registerManifestDiscoveredKinds(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil || reg == nil || !cfg.Plugins.BackendDiscovery.Enabled {
		return nil
	}
	if registrySatisfiesEnabledBackendProfiles(cfg, reg) {
		return nil
	}
	staging, err := os.MkdirTemp("", "go-lip-structural-catalog-*")
	if err != nil {
		return fmt.Errorf("runtimebundle: structural catalog staging: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	resolved, err := ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		return fmt.Errorf("runtimebundle: structural plugin catalog: %w", err)
	}
	if resolved.CatalogErr != nil {
		closeVerifiedArtifacts(resolved.TrustBySafe)
		return fmt.Errorf("runtimebundle: structural plugin catalog: %w", resolved.CatalogErr)
	}
	for _, exp := range resolved.Installable {
		kind := strings.TrimSpace(exp.Kind)
		if kind == "" {
			continue
		}
		if reg.HasBackend(kind) {
			closeVerifiedArtifacts(resolved.TrustBySafe)
			return fmt.Errorf("runtimebundle: structural plugin catalog: duplicate backend registration: %s", kind)
		}
		profile := exp.Profile
		if err := reg.RegisterDiscoveredBackend(
			kind,
			manifestOnlyStubFactory(kind),
			profile,
			pluginreg.BackendReloadPolicy{AllowsCandidateOverlap: true},
		); err != nil {
			closeVerifiedArtifacts(resolved.TrustBySafe)
			return fmt.Errorf("runtimebundle: structural manifest registration: %w", err)
		}
	}
	closeVerifiedArtifacts(resolved.TrustBySafe)
	return nil
}

func manifestOnlyStubFactory(kind string) func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
	kind = strings.TrimSpace(kind)
	return func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, fmt.Errorf("runtimebundle: manifest-only kind %q must not build backends during structural validation", kind)
	}
}
