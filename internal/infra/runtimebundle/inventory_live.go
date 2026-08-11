package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/httpclient"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// inventoryLiveSnapshot holds compiled model-runtime state for operator projection.
type inventoryLiveSnapshot struct {
	backends map[string]execbackend.Backend
	runtime  *modelregistry.Runtime
	registry *modelregistry.Registry
	ledger   *ResourceLedger
}

func (s *inventoryLiveSnapshot) Close(ctx context.Context) error {
	if s == nil || s.ledger == nil {
		return nil
	}
	return s.ledger.Close(ctx)
}

func (s *inventoryLiveSnapshot) compatibleInputs() standardplugins.CompatibleLiveInputs {
	if s == nil {
		return standardplugins.CompatibleLiveInputs{}
	}
	return standardplugins.CompatibleLiveInputs{
		Backends: s.backends,
		Registry: s.registry,
		Runtime:  s.runtime,
	}
}

func loadInventoryLiveSnapshot(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry) (*inventoryLiveSnapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("runtimebundle: nil context")
	}
	if cfg == nil || reg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config or registry")
	}
	ledger := NewResourceLedger()
	bctx := buildContext{
		Cfg:    cfg,
		Log:    slog.Default(),
		Parent: ctx,
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Startup:        StartupOptions{StartupContext: ctx},
			Infra:          InfraOptions{HTTPClient: httpclient.Standard()},
		},
		Ledger: ledger,
	}
	model, err := buildModelRuntime(bctx, bctx.Opts.Infra.HTTPClient)
	if err != nil {
		_ = ledger.Rollback(ctx)
		return nil, err
	}
	return &inventoryLiveSnapshot{
		backends: model.Backends,
		runtime:  model.RegistryRuntime,
		registry: model.Registry,
		ledger:   ledger,
	}, nil
}

func compatibleProjector(cfg *config.Config, live *inventoryLiveSnapshot) diag.CompatibleBackendProjector {
	if live == nil || live.runtime == nil {
		return standardplugins.ProjectCompatibleBackendRows
	}
	inputs := live.compatibleInputs()
	return func(c *config.Config) []diag.CompatibleBackendRow {
		return standardplugins.ProjectCompatibleBackendRowsLive(c, inputs)
	}
}

func inventoryProjectionForOperator(cfg *config.Config, live *inventoryLiveSnapshot) *diag.InventoryProjection {
	return inventoryProjectionForOperatorWithProjector(cfg, compatibleProjector(cfg, live))
}

func inventoryProjectionForOperatorWithProjector(cfg *config.Config, projector diag.CompatibleBackendProjector) *diag.InventoryProjection {
	compatible := projector(cfg)
	projection := diag.ProjectInventoryDiagnostics(cfg, compatible, standardplugins.StandardDiagnosticProjectors())
	return &projection
}

func inventorySnapshotForOperator(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []lipsdk.Registration, live *inventoryLiveSnapshot) (diag.InventorySnapshot, error) {
	return inventorySnapshotForOperatorWithProjector(ctx, cfg, reg, registrations, compatibleProjector(cfg, live))
}

func inventorySnapshotForOperatorWithProjector(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []lipsdk.Registration, projector diag.CompatibleBackendProjector) (diag.InventorySnapshot, error) {
	return inventorySnapshotForOperatorWithProjection(ctx, cfg, reg, registrations, inventoryProjectionForOperatorWithProjector(cfg, projector))
}

func inventorySnapshotForOperatorWithProjection(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []lipsdk.Registration, projection *diag.InventoryProjection) (diag.InventorySnapshot, error) {
	return diag.InventorySnapshotForConfig(ctx, cfg, &diag.InventoryExtras{
		Reg:           reg,
		Registrations: registrations,
		Precomputed:   projection,
	})
}

func routesSnapshotFrom(cfg *config.Config, reg *pluginreg.Registry, live *inventoryLiveSnapshot) (RoutesSnapshot, error) {
	if live != nil && live.runtime != nil {
		return routesSnapshotFromCompatible(cfg, reg, standardplugins.ProjectCompatibleBackendRowsLive(cfg, live.compatibleInputs()))
	}
	return routesSnapshotFromCompatible(cfg, reg, standardplugins.ProjectCompatibleBackendRows(cfg))
}

func tryLoadInventoryLiveSnapshot(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry) (*inventoryLiveSnapshot, error) {
	snap, err := loadInventoryLiveSnapshot(ctx, cfg, reg)
	if err != nil {
		return nil, err
	}
	return snap, nil
}
