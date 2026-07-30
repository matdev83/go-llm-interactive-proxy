package runtimebundle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestCompatibleDiagnostics_inventoryAndRoutesExposeLiveHealth(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)

	ctx := context.Background()
	regInput := runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	}
	inv, err := runtimebundle.InspectInventory(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.CompatibleBackends) != 1 {
		t.Fatalf("inventory compatible_backends=%d", len(inv.CompatibleBackends))
	}
	health := inv.CompatibleBackends[0].InventoryHealth
	if health == nil {
		t.Fatal("expected live inventory_health")
	}
	if health.ModelCount != 1 {
		t.Fatalf("model_count=%d health=%+v", health.ModelCount, health)
	}
	if health.Source != "static_inline" {
		t.Fatalf("source=%q", health.Source)
	}
	if len(health.SampleModels) != 1 || health.SampleModels[0].Prefix != "compat-diag" {
		t.Fatalf("samples=%+v", health.SampleModels)
	}

	routes, err := runtimebundle.InspectRoutes(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if routes.CompatibleBackends[0].InventoryHealth == nil {
		t.Fatal("routes missing live inventory_health")
	}
}

func TestCompatibleDiagnostics_inventoryAndRoutesExposeBuiltInOrigin(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)

	ctx := context.Background()
	regInput := runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	}
	routes, err := runtimebundle.InspectRoutes(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes.CompatibleBackends) != 1 {
		t.Fatalf("routes compatible_backends=%d", len(routes.CompatibleBackends))
	}
	if routes.CompatibleBackends[0].Origin != "built_in_compatible" {
		t.Fatalf("origin=%q", routes.CompatibleBackends[0].Origin)
	}

	inv, err := runtimebundle.InspectInventory(ctx, regInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.CompatibleBackends) != 1 {
		t.Fatalf("inventory compatible_backends=%d", len(inv.CompatibleBackends))
	}
	if inv.CompatibleBackends[0].Prefix != "compat-diag" {
		t.Fatalf("prefix=%q", inv.CompatibleBackends[0].Prefix)
	}
}

func TestCompatibleDiagnostics_inspectDistinguishesFromExternalPlugins(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)
	prep, err := runtimebundle.PrepareInspect(context.Background(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prep.Close() })

	rep, err := runtimebundle.InspectBackendPlugins(prep.Config, prep.Registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.CompatibleBackends) != 1 {
		t.Fatalf("compatible_backends=%d", len(rep.CompatibleBackends))
	}
	var factoryEntry, instanceEntry bool
	for _, e := range rep.Entries {
		if e.Kind == "custom-openai-legacy-compatible" && e.Source == "built_in_compatible" {
			if e.InstanceID == "" {
				factoryEntry = true
			} else if e.InstanceID == "compat-diag" {
				instanceEntry = true
				if e.ActivationRequired {
					t.Fatal("compatible instance must not require plugin activation")
				}
			}
		}
		if e.Source == "discovered" && strings.Contains(e.Kind, "custom-openai") {
			t.Fatalf("compatible kind must not appear as discovered plugin: %+v", e)
		}
	}
	if !factoryEntry || !instanceEntry {
		t.Fatalf("inspect entries missing factory=%v instance=%v entries=%+v", factoryEntry, instanceEntry, rep.Entries)
	}
}

func TestCompatibleDiagnostics_checkConfigNoProviderRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "must not be called", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	path := writeCompatibleDiagnosticsConfig(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "https://example.test/v1", srv.URL+"/v1", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("check-config validation: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider requests during check-config = %d", hits.Load())
	}
}

func TestCompatibleDiagnostics_inventoryJSONSecretSafe(t *testing.T) {
	t.Parallel()
	path := writeCompatibleDiagnosticsConfig(t)
	inv, err := runtimebundle.InspectInventory(context.Background(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, "sk-") {
		t.Fatalf("inventory json leaked secret-like value: %s", body)
	}
}

func writeCompatibleDiagnosticsConfig(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := fmt.Sprintf(`    - id: compat-diag
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-diag
        base_url: https://example.test/v1
        api_key_env_var_root: COMPAT_DIAG_KEY
        tokenizer: o200k_base
        max_concurrent_requests: 3
        models:
          source: inline
          items:
            - canonical_id: compat-diag/model-a
              native_id: model-a
`)
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
