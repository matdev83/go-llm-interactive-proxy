package acp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	backend "github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/acp"
)

// TestExternalParity_ProfileFixtureLocksSharedContract compares retained root
// ACP identity against connectors/acp/testdata/parity_profile.json (filesystem
// golden; root cannot import the external module without go.work).
func TestExternalParity_ProfileFixtureLocksSharedContract(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "connectors", "acp", "testdata", "parity_profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		FactoryKind   string   `json:"factory_kind"`
		RoutePrefixes []string `json:"route_prefixes"`
		Streaming     bool     `json:"streaming"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if want.FactoryKind != backend.ID {
		t.Fatalf("factory_kind=%q root ID=%q", want.FactoryKind, backend.ID)
	}
	if len(want.RoutePrefixes) != 1 || want.RoutePrefixes[0] != backend.ID {
		t.Fatalf("routes=%v", want.RoutePrefixes)
	}
	if !want.Streaming {
		t.Fatal("fixture streaming")
	}
}
