package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

// integrationTierExtraFiles are integration-tagged conformance sources beyond ParitySuiteGoFiles.
var integrationTierExtraFiles = []string{
	"matrix_test.go",
	"conformance_text_test.go",
	"conformance_tools_test.go",
	"conformance_multimodal_test.go",
	"backend_credentials_test.go",
	"conformance_stream_authenticated_test.go",
	"migration_test.go",
	"release_gates_test.go",
}

func TestConformance_integrationTier_sourceFilesPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "testkit", "conformance")
	for _, name := range ParitySuiteGoFiles {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing parity suite file %s: %v", path, err)
		}
		if st.IsDir() {
			t.Fatalf("expected file not directory: %s", path)
		}
	}
	for _, name := range integrationTierExtraFiles {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing integration-tier conformance source %s: %v", path, err)
		}
		if st.IsDir() {
			t.Fatalf("expected file not directory: %s", path)
		}
	}
}
