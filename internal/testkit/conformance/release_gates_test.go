package conformance

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

// Expected migration goldens (Req. 15.13). Keep in sync with docs/release-gates.md.
var expectedMigrationGoldenJSON = []string{
	"python_lip_anthropic_messages_nonstream.json",
	"python_lip_openai_responses_http_nonstream.json",
	"python_lip_openai_responses_http_streaming.json",
}

func TestMigrationGoldenFixtureInventory(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "testdata", "migration")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var jsonFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".json" {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	if len(jsonFiles) != len(expectedMigrationGoldenJSON) {
		t.Fatalf("migration JSON count: got %d want %d; files=%v", len(jsonFiles), len(expectedMigrationGoldenJSON), jsonFiles)
	}
	for _, want := range expectedMigrationGoldenJSON {
		if !slices.Contains(jsonFiles, want) {
			t.Fatalf("missing expected migration golden %q (have %v)", want, jsonFiles)
		}
	}
}
