package diag_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/refautoappend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftoolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reftraffictranscript"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

var _ diag.FeatureRegistry = (*pluginreg.Registry)(nil)

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
	if len(snap.Extensions.LegalPipeline) != 13 {
		t.Fatalf("extensions.legal_pipeline: want 13 got %d", len(snap.Extensions.LegalPipeline))
	}
	if len(snap.Extensions.Stages) != 13 {
		t.Fatalf("extensions.stages: want 13 got %d", len(snap.Extensions.Stages))
	}
	for _, st := range snap.Extensions.Stages {
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.DefaultFailure) == "" {
			t.Fatalf("stage missing id or default_failure: %+v", st)
		}
	}
	pipeline := strings.Join(snap.Extensions.LegalPipeline, " ")
	for _, needle := range []string{"tool_catalog_filter", "pre_request_admission", "completion_gating", "traffic_observation", "session_open"} {
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

func TestInventoryHandler_withRegistry_refToolPolicy_listsCatalogFilterAndToolPolicy(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      reftoolpolicy.ID,
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
	var toolCat, toolReact []string
	for _, occ := range f0.StageOccupancy {
		switch occ.StageID {
		case "tool_catalog_filter":
			toolCat = append(toolCat, occ.HandlerIDs...)
		case "tool_event_reaction":
			toolReact = append(toolReact, occ.HandlerIDs...)
		}
	}
	hasPolicy := false
	hasFilter := false
	hasReactor := false
	for _, id := range toolCat {
		if strings.Contains(id, "tool_catalog:"+reftoolpolicy.ID+"-filter") {
			hasFilter = true
		}
	}
	for _, id := range toolReact {
		if strings.Contains(id, "tool_policy:"+reftoolpolicy.ID+"-tool-policy") {
			hasPolicy = true
		}
		if id == reftoolpolicy.ID+"-reactor" {
			hasReactor = true
		}
	}
	if !hasFilter {
		t.Fatalf("want catalog filter in occupancy, got tool_catalog %#v", toolCat)
	}
	if !hasPolicy {
		t.Fatalf("want tool_policy in occupancy, got tool_event_reaction %#v", toolReact)
	}
	if !hasReactor {
		t.Fatalf("want reactor id in occupancy, got tool_event_reaction %#v", toolReact)
	}
	if !f0.Privileges.AuxiliaryRequests {
		t.Fatal("want auxiliary_requests for catalog shaping")
	}
}

func TestInventoryHandler_refTrafficTranscript_inventoryJSONOmitsDefaultRedactSubstring(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := pluginreg.InstallStandardBundleOn(reg, pluginreg.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{
				ID:      reftraffictranscript.ID,
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
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Default proof config redacts this substring on traffic observer path; inventory must not echo it.
	if strings.Contains(string(body), "REF_SECRET") {
		t.Fatalf("inventory leaked default redaction substring")
	}
	var snap diag.InventorySnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Extensions.Features) != 1 {
		t.Fatalf("features: %d", len(snap.Extensions.Features))
	}
	f0 := snap.Extensions.Features[0]
	if f0.BundleError != "" {
		t.Fatalf("bundle_error: %s", f0.BundleError)
	}
	if !f0.Privileges.RawCapture {
		t.Fatal("want raw_capture privilege flagged")
	}
	seenUsageObserver := false
	for _, occ := range f0.StageOccupancy {
		if occ.StageID != "traffic_observation" {
			continue
		}
		for _, id := range occ.HandlerIDs {
			if strings.HasPrefix(id, "usage_observer:") {
				seenUsageObserver = true
			}
		}
	}
	if !seenUsageObserver {
		t.Fatalf("want usage_observer in traffic_observation occupancy: %#v", f0.StageOccupancy)
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

func TestInventorySnapshotForConfig_matchesInventoryHandlerJSON(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "anthropic", Enabled: false}},
			Features:  []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ctx := context.Background()
	direct, err := diag.InventorySnapshotForConfig(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ih, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	ih.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("handler status %d body %s", rr.Code, rr.Body.String())
	}
	var viaHTTP diag.InventorySnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &viaHTTP); err != nil {
		t.Fatal(err)
	}
	dj, err := json.Marshal(direct)
	if err != nil {
		t.Fatal(err)
	}
	hj, err := json.Marshal(viaHTTP)
	if err != nil {
		t.Fatal(err)
	}
	if string(dj) != string(hj) {
		t.Fatalf("JSON mismatch\ndirect=%s\nhttp  =%s", dj, hj)
	}
}

func TestInventorySnapshotForConfig_nilContext(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
		},
	}
	//nolint:staticcheck // explicit nil context: contract must reject nil without panicking
	_, err := diag.InventorySnapshotForConfig(nil, cfg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "diag: inventory snapshot for config: nil context"; err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}

func TestInventorySnapshotForConfig_nilConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := diag.InventorySnapshotForConfig(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "diag: inventory snapshot for config: nil config"; err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}
