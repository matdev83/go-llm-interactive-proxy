package qa

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The external enterprise-module fixture must stay on the canonical
// registration surface and off the retired Options.Provider / Options.Rater
// fields. This asserts the Go fixture itself; operator-facing wording is
// reviewed by humans rather than pinned by a test.
func TestExternal_EnterpriseModuleCanonicalRegistrations(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src := readDoc(t, root, "testdata", "enterprise_module", "main.go")
	for _, needle := range []string{
		"RequestRegistrations",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("enterprise_module must use canonical %s", needle)
		}
	}
	for _, forbidden := range []string{
		"RequestProviders",
		"AttemptProviders",
		"ConcurrencyProvider",
		"ProviderDescriptors",
		"legacy-production-rater",
		"legacy_options",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("enterprise_module must not mention deleted legacy API %q", forbidden)
		}
	}
	// Bare Options.Rater field usage.
	if regexp.MustCompile(`(?m)^\s*Rater\s*:`).MatchString(src) {
		t.Fatal("enterprise_module must not use deleted Options.Rater field")
	}
	if strings.Contains(src, "internal/") {
		t.Fatal("enterprise_module must not import internal/")
	}
}

func readDoc(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	return readDocPath(t, path)
}

func readDocPath(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
