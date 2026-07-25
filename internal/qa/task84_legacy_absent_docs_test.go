package qa

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Task 8.4: legacy public Options fields are removed. Docs and external fixtures
// must advertise canonical registrations only (requirements 10.5–10.10, 12.8).

func TestDocs_LegacyAbsent_NoCurrentMajorLegacySupportClaims(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	guidePath := filepath.Join(root, "docs", "lipruntime-options-migration.md")
	if _, err := os.Stat(guidePath); err == nil {
		t.Fatal("docs/lipruntime-options-migration.md must be deleted after Task 8.4 removal")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat migration guide: %v", err)
	}

	staleClaims := []string{
		"remain source-compatible",
		"current-major compatibility only",
		"current major source compatibility only",
		"Legacy fields (current major only)",
		"quarantined in `pkg/lipruntime/legacy_options.go`",
		"Final legacy-support release",
	}
	docs := []string{
		filepath.Join(root, "README.md"),
		filepath.Join(root, "docs", "enterprise-extension-boundaries.md"),
		filepath.Join(root, "docs", "extension-platform-authoring.md"),
		filepath.Join(root, "docs", "runtime-flow.md"),
	}
	for _, path := range docs {
		text := readDocPath(t, path)
		lower := strings.ToLower(text)
		for _, claim := range staleClaims {
			if strings.Contains(text, claim) || strings.Contains(lower, strings.ToLower(claim)) {
				t.Fatalf("%s still claims legacy support: %q", path, claim)
			}
		}
		if strings.Contains(text, "lipruntime-options-migration.md") {
			t.Fatalf("%s still links to deleted migration guide", path)
		}
	}
}

func TestDocs_Options_CanonicalRegistrationLanguage(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	checks := map[string][]string{
		"README.md": {
			"RequestRegistrations",
			"AttemptRegistrations",
			"ConcurrencyRegistration",
			"RaterRegistrations",
		},
		"docs/enterprise-extension-boundaries.md": {
			"RequestRegistrations",
			"AttemptRegistrations",
			"ConcurrencyRegistration",
			"RaterRegistrations",
			"pkg/lipruntime",
		},
		"docs/extension-platform-authoring.md": {
			"RequestRegistrations",
			"canonical",
		},
		"docs/runtime-flow.md": {
			"lipruntime.Build",
			"registration",
			"BuildHost",
		},
	}
	for rel, needles := range checks {
		parts := strings.Split(rel, "/")
		text := readDoc(t, root, parts...)
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s missing canonical wording %q", rel, needle)
			}
		}
	}
}

func TestExternal_EnterpriseModuleCanonicalRegistrations(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	src := readDoc(t, root, "testdata", "enterprise_module", "main.go")
	for _, needle := range []string{
		"RequestRegistrations",
		"RaterRegistrations",
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
	// Bare Options.Rater field usage (not RaterRegistrations / reg.Rater).
	if regexp.MustCompile(`(?m)^\s*Rater\s*:`).MatchString(src) {
		t.Fatal("enterprise_module must not use deleted Options.Rater field")
	}
	if strings.Contains(src, "internal/") {
		t.Fatal("enterprise_module must not import internal/")
	}
}

func TestLegacyAbsent_DocsDoNotAdvertiseDeletedFieldsAsCurrent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Mentions of deleted field names are allowed only as explicit absence/
	// history wording, not as "use these fields" examples. Require that any
	// occurrence in operator docs is paired with removal/absent language.
	for _, rel := range []string{
		"docs/enterprise-extension-boundaries.md",
		"docs/extension-platform-authoring.md",
		"docs/runtime-flow.md",
		"README.md",
	} {
		parts := strings.Split(rel, "/")
		text := readDoc(t, root, parts...)
		for _, field := range []string{
			"RequestProviders",
			"AttemptProviders",
			"ProviderDescriptors",
		} {
			if !strings.Contains(text, field) {
				continue
			}
			// If mentioned, must clearly say removed/deleted/absent — not "prefer" or "migrate using".
			idx := strings.Index(text, field)
			windowStart := idx - 80
			if windowStart < 0 {
				windowStart = 0
			}
			windowEnd := idx + 120
			if windowEnd > len(text) {
				windowEnd = len(text)
			}
			window := strings.ToLower(text[windowStart:windowEnd])
			if !strings.Contains(window, "removed") &&
				!strings.Contains(window, "deleted") &&
				!strings.Contains(window, "absent") &&
				!strings.Contains(window, "no longer") {
				t.Fatalf("%s mentions %q without removal wording near it: %q", rel, field, text[windowStart:windowEnd])
			}
		}
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
