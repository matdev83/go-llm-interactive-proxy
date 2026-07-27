//go:build precommit

package identity_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"github.com/matdev83/go-llm-interactive-proxy/internal/refclient/refclienttest"
)

func TestSpecBundle_identityScenarios_referenceTests(t *testing.T) {
	t.Parallel()
	// Repository root (filesystem), not go list / go.work membership: PackageRel may
	// point at external connector modules under connectors/ that are outside the root module.
	root := refclienttest.ModuleRoot(t)
	docBytes, err := os.ReadFile(filepath.Join(root, "docs", "spec-bundle-identity-scenarios.md"))
	if err != nil {
		t.Fatal(err)
	}
	docText := string(docBytes)
	for _, spec := range identity.SpecBundleIdentityScenarios() {
		if spec.ID == "" || spec.InvariantSummary == "" || spec.TestName == "" || spec.PackageRel == "" {
			t.Fatalf("incomplete scenario: %#v", spec)
		}
		if strings.Contains(spec.PackageRel, `\`) || strings.HasPrefix(spec.PackageRel, "/") || strings.Contains(spec.PackageRel, "..") {
			t.Fatalf("scenario %s PackageRel must be forward-slash repository-relative: %q", spec.ID, spec.PackageRel)
		}
		dir := filepath.Join(append([]string{root}, strings.Split(spec.PackageRel, "/")...)...)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("scenario %s package %q: %v", spec.ID, spec.PackageRel, err)
		}
		var blobs strings.Builder
		foundTestFile := false
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, "_test.go") {
				continue
			}
			foundTestFile = true
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			blobs.Write(b)
			blobs.WriteByte('\n')
		}
		if !foundTestFile {
			t.Fatalf("scenario %s package %q has no *_test.go files on disk", spec.ID, spec.PackageRel)
		}
		src := blobs.String()
		needle := "func " + spec.TestName
		if !strings.Contains(src, needle) {
			t.Fatalf("scenario %s references missing test %q in %s", spec.ID, spec.TestName, spec.PackageRel)
		}
		if !strings.Contains(docText, spec.ID) {
			t.Fatalf("docs/spec-bundle-identity-scenarios.md must mention scenario id %q", spec.ID)
		}
		if !strings.Contains(docText, spec.TestName) {
			t.Fatalf("docs/spec-bundle-identity-scenarios.md must mention test %q", spec.TestName)
		}
	}
}
