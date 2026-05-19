package stdhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
)

func TestMountedInventoryJSON_includesExtensionTruthBlock_RED(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ih, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	const path = "/debug/inventory"
	mux.Handle(path, diag.WrapDiagnosticsProtect("", ih))

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Extensions json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Extensions) == 0 || string(envelope.Extensions) == "null" {
		t.Fatal("RED stage four: mounted inventory must include non-null extensions block (R14 / design section 14)")
	}
	var ext diag.InventoryExtensions
	if err := json.Unmarshal(envelope.Extensions, &ext); err != nil {
		t.Fatal(err)
	}
	if len(ext.LegalPipeline) != 13 || len(ext.Stages) != 13 {
		t.Fatalf("extensions contract: want 13 pipeline stages each, got pipeline=%d stages=%d", len(ext.LegalPipeline), len(ext.Stages))
	}
	for _, st := range ext.Stages {
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.DefaultFailure) == "" {
			t.Fatalf("stage missing id or default_failure: %+v", st)
		}
	}
}
