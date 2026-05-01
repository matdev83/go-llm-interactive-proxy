package runtimebundle_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestInventorySnapshotForOperator_matchesInventorySnapshotForConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "stub1", Kind: "local-stub", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	regs := []lipsdk.Registration{}
	op, err := runtimebundle.InventorySnapshotForOperator(ctx, cfg, reg, regs)
	if err != nil {
		t.Fatal(err)
	}
	want, err := diag.InventorySnapshotForConfig(ctx, cfg, &diag.InventoryExtras{
		Reg:           reg,
		Registrations: regs,
	})
	if err != nil {
		t.Fatal(err)
	}
	ob, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(ob) != string(wb) {
		t.Fatalf("JSON mismatch\noperator=%s\ndirect   =%s", ob, wb)
	}
}
