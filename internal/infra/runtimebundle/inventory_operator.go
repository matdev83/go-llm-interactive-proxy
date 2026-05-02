package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// InventorySnapshotForOperator builds the same JSON inventory model as stdhttp diagnostics inventory.
// It keeps cmd composition roots from importing internal/core/diag directly (hexagonal migration
// baseline). Semantics match [diag.InventorySnapshotForConfig] with equivalent [diag.InventoryExtras]
// (see inventory_operator_test.go).
func InventorySnapshotForOperator(
	ctx context.Context,
	cfg *config.Config,
	reg *pluginreg.Registry,
	registrations []lipsdk.Registration,
) (diag.InventorySnapshot, error) {
	return diag.InventorySnapshotForConfig(ctx, cfg, &diag.InventoryExtras{
		Reg:           reg,
		Registrations: registrations,
	})
}
