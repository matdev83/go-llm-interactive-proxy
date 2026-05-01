//go:build precommit

package hooks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

func TestSpecBundle_hookScenarios_referenceTests(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "core", "hooks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var blobs strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		blobs.Write(b)
		blobs.WriteByte('\n')
	}
	src := blobs.String()
	docBytes, err := os.ReadFile(filepath.Join(root, "docs", "spec-bundle-hook-scenarios.md"))
	if err != nil {
		t.Fatal(err)
	}
	docText := string(docBytes)
	for _, spec := range hooks.SpecBundleHookScenarios() {
		if spec.ID == "" || spec.InvariantSummary == "" || spec.TestName == "" {
			t.Fatalf("incomplete scenario: %#v", spec)
		}
		needle := "func " + spec.TestName
		if !strings.Contains(src, needle) {
			t.Fatalf("scenario %s references missing test %q (expected %q in hooks *_test.go)", spec.ID, spec.TestName, needle)
		}
		if !strings.Contains(docText, spec.ID) {
			t.Fatalf("docs/spec-bundle-hook-scenarios.md must mention scenario id %q", spec.ID)
		}
		if !strings.Contains(docText, spec.TestName) {
			t.Fatalf("docs/spec-bundle-hook-scenarios.md must mention test %q", spec.TestName)
		}
	}
}
