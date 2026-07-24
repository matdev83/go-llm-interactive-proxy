package runtimebundle

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// InspectInput configures [InspectRoutes] / [InspectInventory] (no logger/tracing/runtime).
type InspectInput struct {
	ConfigPath              string
	Mandatory               []lipsdk.Requirement
	StreamRecoveryOverrides config.StreamRecoveryOverrides
}

// InspectRoutes loads one effective snapshot and projects a routes-only read model.
func InspectRoutes(ctx context.Context, in InspectInput) (RoutesSnapshot, error) {
	return inspectRoutes(ctx, in, LoadBootstrapEffectiveWithSource)
}

func inspectRoutes(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (RoutesSnapshot, error) {
	cfg, reg, _, err := prepareInspect(ctx, in, loadEffective)
	if err != nil {
		return RoutesSnapshot{}, err
	}
	return RoutesSnapshotFrom(cfg, reg)
}

// InspectInventory loads one effective snapshot and projects an inventory-only read model.
func InspectInventory(ctx context.Context, in InspectInput) (diag.InventorySnapshot, error) {
	return inspectInventory(ctx, in, LoadBootstrapEffectiveWithSource)
}

func inspectInventory(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (diag.InventorySnapshot, error) {
	cfg, reg, regs, err := prepareInspect(ctx, in, loadEffective)
	if err != nil {
		return diag.InventorySnapshot{}, err
	}
	return InventorySnapshotForOperator(ctx, cfg, reg, regs)
}

// prepareInspect is the private shared inspect pipeline. loadEffective must be
// supplied by the caller (never a package-global fallback) so config_load sees
// no new startup-load alias.
func prepareInspect(ctx context.Context, in InspectInput, loadEffective bootstrapEffectiveLoader) (*config.Config, *pluginreg.Registry, []lipsdk.Registration, error) {
	if ctx == nil {
		return nil, nil, nil, fmt.Errorf("runtimebundle: nil context")
	}
	if loadEffective == nil {
		return nil, nil, nil, fmt.Errorf("runtimebundle: nil effective loader")
	}
	path := strings.TrimSpace(in.ConfigPath)
	if path == "" {
		return nil, nil, nil, fmt.Errorf("runtimebundle: empty config path")
	}
	effective, _, _, err := loadEffective(ctx, path, in.StreamRecoveryOverrides)
	if err != nil {
		return nil, nil, nil, err
	}
	if effective == nil || effective.Config == nil {
		return nil, nil, nil, fmt.Errorf("runtimebundle: nil effective config")
	}
	reg, regs, err := installRegistryAndRegistrations(effective.Config, in.Mandatory)
	if err != nil {
		return nil, nil, nil, err
	}
	return effective.Config, reg, regs, nil
}
