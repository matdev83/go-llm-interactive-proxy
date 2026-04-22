package diag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
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
	ih, err := diag.InventoryHandler(cfg)
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
}
