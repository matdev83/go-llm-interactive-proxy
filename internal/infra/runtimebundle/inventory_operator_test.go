package runtimebundle_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

func TestInventorySnapshotForOperator_matchesConfigSnapshotWithExtras(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Features: []config.PluginConfig{{
				ID:      refautoappend.ID,
				Enabled: true,
			}},
		},
	}
	extras := config.RegistrationsFromConfig(cfg)
	ctx := context.Background()

	got, err := runtimebundle.InventorySnapshotForOperator(ctx, cfg, reg, extras)
	if err != nil {
		t.Fatal(err)
	}
	want, err := diag.InventorySnapshotForConfig(ctx, cfg, &diag.InventoryExtras{
		Reg:                          reg,
		Registrations:                extras,
		InstanceDiagnosticProjectors: standardplugins.StandardDiagnosticProjectors(),
	})
	if err != nil {
		t.Fatal(err)
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("snapshot mismatch\ngot  %s\nwant %s", gotJSON, wantJSON)
	}
}
