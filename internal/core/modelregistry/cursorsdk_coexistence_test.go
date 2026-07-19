package modelregistry_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorcliacp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/cursorsdk/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestRegistry_CursorSDKAndCLIACPShareCanonicalRetainProvenance(t *testing.T) {
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

	shared := byNative["composer-2-fast"]
	acpInv := modelinventory.StaticProvider{Models: []modelinventory.Model{{
		CanonicalID: shared.CanonicalID,
		NativeID:    shared.NativeID,
		DisplayName: shared.DisplayName,
	}}}
	sdkInv := modelinventory.StaticProvider{Models: sdkModels}

	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{
		{
			BackendID:       "cursor-sdk-a",
			Kind:            cursorsdk.ID,
			BackendPrefixes: []string{cursorsdk.ID},
			Provider:        sdkInv,
		},
		{
			BackendID:       "cursor-acp-b",
			Kind:            cursorcliacp.ID,
			BackendPrefixes: []string{cursorcliacp.ID},
			Provider:        acpInv,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, ok := built.Registry.Lookup(shared.CanonicalID)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false", shared.CanonicalID)
	}
	if len(got) != 2 {
		t.Fatalf("len(Lookup) = %d, want 2", len(got))
	}
	byKind := map[string]modelregistry.BackendModel{}
	for _, ref := range got {
		byKind[ref.Kind] = ref
	}
	sdkRef, ok := byKind[cursorsdk.ID]
	if !ok {
		t.Fatal("missing cursorsdk provenance row")
	}
	acpRef, ok := byKind[cursorcliacp.ID]
	if !ok {
		t.Fatal("missing cursorcliacp provenance row")
	}
	if sdkRef.BackendID != "cursor-sdk-a" || acpRef.BackendID != "cursor-acp-b" {
		t.Fatalf("BackendID mismatch: sdk=%q acp=%q", sdkRef.BackendID, acpRef.BackendID)
	}
	if sdkRef.Kind != cursorsdk.ID || acpRef.Kind != cursorcliacp.ID {
		t.Fatalf("Kind mismatch: %#v %#v", sdkRef.Kind, acpRef.Kind)
	}
	if sdkRef.CanonicalID != shared.CanonicalID || acpRef.CanonicalID != shared.CanonicalID {
		t.Fatalf("canonical mismatch: %#v %#v", sdkRef.CanonicalID, acpRef.CanonicalID)
	}
	if sdkRef.NativeID != shared.NativeID || acpRef.NativeID != shared.NativeID {
		t.Fatalf("native mismatch: %#v %#v", sdkRef.NativeID, acpRef.NativeID)
	}
}

func loadNormalizedSDKModels(t *testing.T, rows []protocol.ModelRow) []modelinventory.Model {
	t.Helper()
	cfg, err := cursorsdk.Normalize(cursorsdk.Input{
		APIKey:           "coexist-test-key",
		BridgeExecutable: os.Args[0],
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	be := cursorsdk.NewScaffold(cfg).WithModelListSource(cursorsdk.StaticModelListSource{Rows: rows}).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(snap.Models) != len(rows) {
		t.Fatalf("normalized len = %d, want %d", len(snap.Models), len(rows))
	}
	return snap.Models
}

func TestRegistry_CursorSDKFixtureNormalizeFeedsRegistry(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "backends", "cursorsdk", "testdata", "fixtures", "models_sanitized.json"))
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
	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "cursor-sdk-fixture",
		Kind:            cursorsdk.ID,
		BackendPrefixes: []string{cursorsdk.ID},
		Provider:        modelinventory.StaticProvider{Models: models},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := built.Registry.Lookup("cursor/gpt-5.3-codex"); !ok {
		t.Fatal("expected fixture gpt canonical in registry")
	}
}
