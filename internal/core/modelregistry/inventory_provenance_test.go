package modelregistry_test

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/modelregistry"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)

func TestInventoryProvenance_enrichesPrefixAndCapabilitySource(t *testing.T) {
	t.Parallel()
	inv := modelregistry.BackendInventory{
		BackendID:       "compat-a",
		Kind:            "custom-openai-legacy-compatible",
		BackendPrefixes: []string{"compat-a"},
	}
	rows := []modelregistry.BackendModel{{
		CanonicalID: "compat-a/model-a",
		NativeID:    "model-a",
		BackendID:   "compat-a",
		Kind:        "custom-openai-legacy-compatible",
		Source:      modelinventory.SourceStaticInline,
		LoadedAt:    time.Unix(100, 0).UTC(),
	}}
	got := modelregistry.EnrichBackendModelsForTest(rows, inv)
	if len(got) != 1 {
		t.Fatalf("rows=%d", len(got))
	}
	if got[0].Prefix != "compat-a" {
		t.Fatalf("prefix=%q", got[0].Prefix)
	}
	if got[0].CapabilitySource != modelregistry.CapabilitySourceStaticConfig {
		t.Fatalf("capability=%q", got[0].CapabilitySource)
	}
}

func TestInventoryProvenance_remoteDiscoveryCapability(t *testing.T) {
	t.Parallel()
	inv := modelregistry.BackendInventory{
		BackendID:       "compat-b",
		Kind:            "custom-openai-legacy-compatible",
		BackendPrefixes: []string{"compat-b"},
	}
	rows := []modelregistry.BackendModel{{
		CanonicalID: "compat-b/gpt",
		NativeID:    "gpt",
		Source:      modelinventory.SourceRemote,
	}}
	got := modelregistry.EnrichBackendModelsForTest(rows, inv)
	if got[0].CapabilitySource != modelregistry.CapabilitySourceRemoteDiscovery {
		t.Fatalf("capability=%q", got[0].CapabilitySource)
	}
}
