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

// Parity suite sources (.kiro/specs/llm-api-parity/tasks.md Phase 5). Keep filenames in sync when adding protocols.
var paritySuiteGoFiles = []string{
	"parity_openai_test.go",
	"parity_anthropic_test.go",
	"parity_gemini_test.go",
	"parity_bedrock_test.go",
	"parity_acp_test.go",
}

// parityProtocolEvidence maps every bundled frontend/backend protocol id to at least one
// parity suite source file in this package (llm-api-parity P5.1). Shared families may
// list the same file for multiple ids.
var parityProtocolEvidence = map[string][]string{
	"openai-responses": {"parity_openai_test.go"},
	"openai-legacy":    {"parity_openai_test.go"},
	"anthropic":        {"parity_anthropic_test.go"},
	"gemini":           {"parity_gemini_test.go"},
	"bedrock":          {"parity_bedrock_test.go"},
	"acp":              {"parity_acp_test.go"},
}

func allBundledProtocolIDs(t *testing.T) []string {
	t.Helper()
	m := map[string]struct{}{}
	for _, id := range BundledFrontendIDs() {
		m[id] = struct{}{}
	}
	for _, id := range BundledBackendIDs() {
		m[id] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func TestParityMatrixCompleteness(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "testkit", "conformance")
	for _, id := range allBundledProtocolIDs(t) {
		files, ok := parityProtocolEvidence[id]
		if !ok || len(files) == 0 {
			t.Fatalf("protocol %q missing parityProtocolEvidence entry (extend release_gates_test.go)", id)
		}
		for _, name := range files {
			path := filepath.Join(dir, name)
			st, err := os.Stat(path)
			if err != nil {
				t.Fatalf("protocol %q evidence file %s: %v", id, path, err)
			}
			if st.IsDir() {
				t.Fatalf("expected file not directory: %s", path)
			}
		}
	}
}

func TestParitySuiteSourceFilesPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "testkit", "conformance")
	for _, name := range paritySuiteGoFiles {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing parity suite file %s: %v", path, err)
		}
		if st.IsDir() {
			t.Fatalf("expected file not directory: %s", path)
		}
	}
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
