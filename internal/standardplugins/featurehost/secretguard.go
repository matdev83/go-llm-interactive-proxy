package featurehost

import (
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// Re-exported types from secretguard so runtimebundle does not import dedicated compose package.
type (
	SecretGuardInputs      = secretguard.SecretGuardInputs
	SecretGuardEnvironment = secretguard.Environment
	SingleUserOptions      = secretguard.SingleUserOptions
	MatcherOptions         = secretguard.MatcherOptions
	SecretDecisionObserver = sdk.Observer
)

// SecretGuardRuntime contains the compiled secret guard plane and inventory extras.
type SecretGuardRuntime struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventoryExtras
}

// SecretGuardBuildInput contains inputs for building the secret guard runtime.
type SecretGuardBuildInput struct {
	AccessMode    accessmode.Mode
	Registrations []lipsdk.Registration
	Guards        []sdk.Guard
	Environment   SecretGuardEnvironment
	Inputs        SecretGuardInputs
	// HostInputs carries the bound secret-guard host inputs when a host binding
	// was supplied (nil when absent). Composition overlays explicitly-set host
	// fields onto YAML-decoded options.
	HostInputs       *SecretGuardInputs
	DecisionObserver SecretDecisionObserver
	Logger           *slog.Logger
}

// ValidateSecretGuardRegistrations validates registration requirements for secret guard.
func ValidateSecretGuardRegistrations(regs []lipsdk.Registration) error {
	return secretguard.ValidateRegistrations(regs)
}

// buildSecretGuardRuntime composes the secret guard extension plane and diagnostics inventory.
func buildSecretGuardRuntime(in SecretGuardBuildInput) (*SecretGuardRuntime, error) {
	out, err := secretguard.Compose(secretguard.Input{
		AccessMode:       in.AccessMode,
		Registrations:    in.Registrations,
		Guards:           in.Guards,
		Environment:      in.Environment,
		Inputs:           in.Inputs,
		HostInputs:       in.HostInputs,
		DecisionObserver: in.DecisionObserver,
		Logger:           in.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &SecretGuardRuntime{
		Plane:     out.Plane,
		Inventory: out.Inventory,
	}, nil
}

// secretGuardExecutionContributorID identifies the standard-distribution
// generation binder contribution of the secret-guard execution plane. It
// matches the plane identity so exclusive conflicts attribute correctly.
const secretGuardExecutionContributorID = "secret-guard-execution"

// secretGuardExecutionConfig projects a composed secret-guard runtime onto the
// ordinary secret-guard execution plane published in the generation frozen
// surface. It returns nil when nothing was composed (disabled generation), so
// generic runtimebundle reconstructs the identical plane and inventory purely
// via plane access.
func secretGuardExecutionConfig(out *SecretGuardRuntime) *sdk.ExecutionConfig {
	// The inventory is the exact "composed something" signal: compose sets it
	// if and only if the feature is enabled or guards are present. A disabled
	// generation publishes no execution plane (NilSkip), preserving zero-plane
	// composition for generations without secret-guard posture.
	if out == nil || out.Inventory == nil {
		return nil
	}
	cfg := &sdk.ExecutionConfig{
		MatcherResolver:    out.Plane.MatcherResolver,
		DecisionObserver:   out.Plane.DecisionObserver,
		AuditFailurePolicy: out.Plane.AuditFailurePolicy,
		AccessMode:         out.Plane.AccessMode,
		ConfigVersion:      out.Plane.ConfigVersion,
	}
	if out.Inventory != nil {
		cfg.CatalogEntryCount = out.Inventory.SecretGuardCatalogEntryCount
		cfg.SourceCategories = append([]string(nil), out.Inventory.SecretGuardSourceCategories...)
		cfg.CatalogAction = out.Inventory.SecretGuardAction
	}
	if cfg.IsZero() {
		return nil
	}
	return cfg
}

// bindSecretGuardExecutionPlane publishes the composed secret-guard execution
// posture as an ordinary generation-binder plane inside the frozen surface, so
// generic runtimebundle reconstructs plane and inventory purely via plane access.
func bindSecretGuardExecutionPlane(outPlanes lipfeature.FrozenPlaneSet, sgOut *SecretGuardRuntime) (lipfeature.FrozenPlaneSet, error) {
	execCfg := secretGuardExecutionConfig(sgOut)
	if execCfg == nil {
		return outPlanes, nil
	}
	cs := outPlanes.ToContributions()
	if err := lipfeature.ContributeSource(cs, lipfeature.PlaneSecretGuardExecution, lipfeature.SourceGenerationBinder, secretGuardExecutionContributorID, execCfg); err != nil {
		return lipfeature.FrozenPlaneSet{}, fmt.Errorf("featurehost: secret guard execution plane: %w", err)
	}
	return cs.Freeze(), nil
}

// BuildSecretGuardRuntime builds secret guard runtime through the featurehost facade.
func (r *Runtime) BuildSecretGuardRuntime(in SecretGuardBuildInput) (*SecretGuardRuntime, error) {
	if r == nil {
		return nil, nil
	}
	if in.Logger == nil {
		in.Logger = r.logger
	}
	return buildSecretGuardRuntime(in)
}
