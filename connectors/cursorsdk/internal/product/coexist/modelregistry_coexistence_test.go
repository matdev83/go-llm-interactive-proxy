package coexist_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

// TestInventory_CursorSDKCanonicalNormalization certifies that connector-owned
// SDK model-list normalization produces the expected public modelinventory.Model
// identity fields (CanonicalID / NativeID / DisplayName) for known native IDs.
func TestInventory_CursorSDKCanonicalNormalization(t *testing.T) {
	t.Parallel()

	sdkModels := loadNormalizedSDKModels(t, []protocol.ModelRow{
		{ID: "composer-2-fast", DisplayName: "Composer 2 Fast"},
		{ID: "cursor-composer-2", DisplayName: "Cursor Composer 2"},
		{ID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex"},
	})
	byNative := map[string]modelinventory.Model{}
	for _, m := range sdkModels {
		byNative[m.NativeID] = m
	}
	for _, native := range []string{"composer-2-fast", "cursor-composer-2", "gpt-5.3-codex"} {
		m, ok := byNative[native]
		if !ok {
			t.Fatalf("missing normalized native %q", native)
		}
		if !strings.HasPrefix(m.CanonicalID, "cursor/") {
			t.Fatalf("native %q CanonicalID = %q", native, m.CanonicalID)
		}
	}
	if byNative["cursor-composer-2"].CanonicalID != "cursor/composer-2" {
		t.Fatalf("cursor- strip = %q", byNative["cursor-composer-2"].CanonicalID)
	}
	if byNative["composer-2-fast"].CanonicalID != "cursor/composer-2-fast" {
		t.Fatalf("bare composer canonical = %q", byNative["composer-2-fast"].CanonicalID)
	}
	if byNative["composer-2-fast"].DisplayName != "Composer 2 Fast" {
		t.Fatalf("bare composer DisplayName = %q", byNative["composer-2-fast"].DisplayName)
	}
	if byNative["gpt-5.3-codex"].CanonicalID != "cursor/gpt-5.3-codex" {
		t.Fatalf("gpt canonical = %q", byNative["gpt-5.3-codex"].CanonicalID)
	}
}

func loadNormalizedSDKModels(t *testing.T, rows []protocol.ModelRow) []modelinventory.Model {
	t.Helper()
	cfg, err := product.Normalize(product.Input{
		APIKey:           "coexist-test-key",
		BridgeExecutable: os.Args[0],
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	be := product.NewScaffold(cfg).WithModelListSource(product.StaticModelListSource{Rows: rows}).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snap.Models) != len(rows) {
		t.Fatalf("normalized len = %d, want %d", len(snap.Models), len(rows))
	}
	return snap.Models
}

func TestInventory_CursorSDKFixtureNormalizeFeedsCanonicalLookup(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", "models_sanitized.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Models []protocol.ModelRow `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	models := loadNormalizedSDKModels(t, doc.Models)
	byCanonical := map[string]modelinventory.Model{}
	for _, m := range models {
		byCanonical[m.CanonicalID] = m
	}
	if _, ok := byCanonical["cursor/gpt-5.3-codex"]; !ok {
		t.Fatal("expected fixture gpt canonical in normalized inventory")
	}
}
