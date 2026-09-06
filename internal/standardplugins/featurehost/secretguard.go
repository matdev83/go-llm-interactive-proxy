package featurehost

import (
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// Re-exported types from secretguardcompose so runtimebundle does not import dedicated compose package.
type SecretGuardInputs = secretguardcompose.SecretGuardInputs
type SecretGuardEnvironment = secretguardcompose.Environment
type SingleUserOptions = secretguardcompose.SingleUserOptions
type MatcherOptions = secretguardcompose.MatcherOptions
type SecretDecisionObserver = sdk.Observer

// SecretGuardRuntime contains the compiled secret guard plane and inventory extras.
type SecretGuardRuntime struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventoryExtras
}

// SecretGuardBuildInput contains inputs for building the secret guard runtime.
type SecretGuardBuildInput struct {
	AccessMode       accessmode.Mode
	Registrations    []lipsdk.Registration
	Guards           []sdk.Guard
	Environment      SecretGuardEnvironment
	Inputs           SecretGuardInputs
	DecisionObserver SecretDecisionObserver
	Logger           *slog.Logger
}

// ValidateSecretGuardRegistrations validates registration requirements for secret guard.
func ValidateSecretGuardRegistrations(regs []lipsdk.Registration) error {
	return secretguardcompose.ValidateRegistrations(regs)
}

// buildSecretGuardRuntime composes the secret guard extension plane and diagnostics inventory.
func buildSecretGuardRuntime(in SecretGuardBuildInput) (*SecretGuardRuntime, error) {
	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:       in.AccessMode,
		Registrations:    in.Registrations,
		Guards:           in.Guards,
		Environment:      in.Environment,
		Inputs:           in.Inputs,
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
