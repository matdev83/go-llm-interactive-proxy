package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

func TestConformanceGoldenCoverageDocPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	path := filepath.Join(root, "docs", "conformance-golden-coverage.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(b)
	if len(strings.TrimSpace(text)) < 80 {
		t.Fatalf("doc unexpectedly short: %s", path)
	}
	for _, id := range AllBundledProtocolIDs() {
		if !strings.Contains(text, "`"+id+"`") && !strings.Contains(text, "| `"+id+"`") {
			t.Fatalf("docs/conformance-golden-coverage.md must mention protocol id %q (table or inline)", id)
		}
	}
	for _, name := range ExpectedMigrationGoldenJSON {
		if !strings.Contains(text, name) {
			t.Fatalf("docs/conformance-golden-coverage.md must mention migration golden %q", name)
		}
	}
	if !strings.Contains(text, "conformance-matrix-evidence.md") {
		t.Fatalf("docs/conformance-golden-coverage.md must link conformance-matrix-evidence.md")
	}
}
