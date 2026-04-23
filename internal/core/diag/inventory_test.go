package diag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestInventoryHandler_returnsPluginRows(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "anthropic", Enabled: false}},
			Features:  []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ih, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Frontends) != 1 || snap.Frontends[0].ID != "openai-responses" {
		t.Fatalf("frontends: %+v", snap.Frontends)
	}
	if len(snap.Extensions.LegalPipeline) != 12 {
		t.Fatalf("extensions.legal_pipeline: want 12 got %d", len(snap.Extensions.LegalPipeline))
	}
	if len(snap.Extensions.Stages) != 12 {
		t.Fatalf("extensions.stages: want 12 got %d", len(snap.Extensions.Stages))
	}
	for _, st := range snap.Extensions.Stages {
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.DefaultFailure) == "" {
			t.Fatalf("stage missing id or default_failure: %+v", st)
		}
	}
	pipeline := strings.Join(snap.Extensions.LegalPipeline, " ")
	for _, needle := range []string{"tool_catalog_filter", "completion_gating", "traffic_observation", "session_open"} {
		if !strings.Contains(pipeline, needle) {
			t.Fatalf("legal_pipeline missing %q: %s", needle, pipeline)
		}
	}
}

func TestInventoryHandler_withRegistry_refAutoappend_hasStageOccupancyAndPrivileges(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      refautoappend.ID,
				Enabled: true,
			}},
		},
	}
	extras := &diag.InventoryExtras{
		Reg:           reg,
		Registrations: config.RegistrationsFromConfig(cfg),
	}
	ih, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Extensions.Features) != 1 {
		t.Fatalf("features: %d", len(snap.Extensions.Features))
	}
	f0 := snap.Extensions.Features[0]
	if f0.BundleError != "" {
		t.Fatalf("bundle_error: %s", f0.BundleError)
	}
	if len(f0.StageOccupancy) == 0 {
		t.Fatal("want non-empty stage_occupancy for ref-autoappend-file with live registry")
	}
	seenSession := false
	for _, occ := range f0.StageOccupancy {
		if occ.StageID == "session_open" {
			seenSession = true
		}
		if occ.Count != len(occ.HandlerIDs) {
			t.Fatalf("occupancy count mismatch: %+v", occ)
		}
	}
	if !seenSession {
		t.Fatalf("expected session_open occupancy, got %#v", f0.StageOccupancy)
	}
}

func TestInventoryHandler_refVerifierPrivilegesExposeCompletionGate(t *testing.T) {
	t.Parallel()
	const refVerifierID = "ref-verifier-stub"
	reg := &pluginreg.Registry{}
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      refVerifierID,
				Enabled: true,
			}},
		},
	}
	extras := &diag.InventoryExtras{
		Reg: reg,
		Registrations: []lipsdk.Registration{{
			Kind:        lipsdk.PluginKindFeature,
			ID:          refVerifierID,
			FactoryKind: refVerifierID,
			Enabled:     true,
		}},
	}
	ih, err := diag.InventoryHandler(cfg, extras)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ih)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var snap diag.InventorySnapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	for _, f := range snap.Extensions.Features {
		if f.InstanceID != refVerifierID {
			continue
		}
		if !f.Privileges.CompletionGate {
			t.Fatal("want completion_gate privilege true for ref-verifier-stub bundle")
		}
		if !f.Privileges.AuxiliaryRequests {
			t.Fatal("want auxiliary_requests privilege true for ref-verifier-stub bundle")
		}
		return
	}
	t.Fatal("ref-verifier feature row missing")
}
