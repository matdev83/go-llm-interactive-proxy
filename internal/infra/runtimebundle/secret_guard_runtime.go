package runtimebundle

import (
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accessmode"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/secretguardcompose"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

type secretGuardRuntime struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventoryExtras
}

func buildSecretGuardRuntime(cfg *config.Config, log *slog.Logger, opts *BuildOptions, regs []lipsdk.Registration) (*secretGuardRuntime, error) {
	if opts == nil {
		return nil, nil
	}
	mode := accessmode.ModeSingleUser
	if cfg != nil {
		var err error
		mode, err = cfg.EffectiveAccessMode()
		if err != nil {
			return nil, err
		}
	}
	var guards []sdk.Guard
	if opts != nil {
		guards = lipfeature.Get(opts.FeaturePlanes, lipfeature.PlaneSecretGuards)
	}
	out, err := secretguardcompose.Compose(secretguardcompose.Input{
		AccessMode:       mode,
		Registrations:    regs,
		Guards:           guards,
		Environment:      opts.Extensions.SecretGuardEnvironment,
		Inputs:           opts.Extensions.SecretGuardInputs,
		DecisionObserver: opts.Extensions.SecretDecisionObserver,
		Logger:           log,
	})
	if err != nil {
		return nil, err
	}
	return &secretGuardRuntime{
		Plane:     out.Plane,
		Inventory: out.Inventory,
	}, nil
}
