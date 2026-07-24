package qa

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Task 8.3: public migration contract + removal-gate wording must stay discoverable
// in docs (requirements 10.7–10.10, 12.8). Hermes owns tasks.md; this gate is the
// durable proof for maintainers and external-module fixtures.

func TestDocs_LegacyOptionsMigrationGuide(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	guidePath := filepath.Join(root, "docs", "lipruntime-options-migration.md")
	guide, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("migration guide missing (Task 8.3): %v", err)
	}
	text := string(guide)

	for _, needle := range []string{
		"RequestProviders",
		"AttemptProviders",
		"ConcurrencyProvider",
		"Rater",
		"ProviderDescriptors",
		"RequestRegistrations",
		"AttemptRegistrations",
		"ConcurrencyRegistration",
		"RaterRegistrations",
		"legacy-production-rater",
		"enterprise_module",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("docs/lipruntime-options-migration.md missing %q", needle)
		}
	}

	if !strings.Contains(text, "last release in the current major line before the next compatible major-version boundary") {
		t.Fatal("migration guide must mark the final legacy-support release without inventing a semver")
	}
	const deletionAnchor = "Next compatible major deletion target"
	anchorIdx := strings.Index(text, deletionAnchor)
	if anchorIdx < 0 {
		t.Fatal("migration guide must name the next compatible major deletion target")
	}
	// Window around the deletion-target heading so field names must appear in
	// removal context, not merely elsewhere in the guide.
	start := anchorIdx
	end := anchorIdx + 900
	if end > len(text) {
		end = len(text)
	}
	deletionWindow := text[start:end]
	for _, deleted := range []string{
		"RequestProviders",
		"AttemptProviders",
		"ConcurrencyProvider",
		"Rater",
		"ProviderDescriptors",
		"legacy-production-rater",
	} {
		if !strings.Contains(deletionWindow, deleted) {
			t.Fatalf("deletion target section must mention %q (near %q)", deleted, deletionAnchor)
		}
	}

	if !regexp.MustCompile(`(?i)legacy-production-rater[\s\S]{0,200}compat`).MatchString(text) &&
		!strings.Contains(text, "compatibility-only") &&
		!strings.Contains(text, "compatibility only") {
		t.Fatal("legacy-production-rater must be labeled compatibility-only")
	}

	assertNoInternalImportExamples(t, text)
}

func TestMigration_LegacyOptionsBoundaryDocs(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	enterprise := readDoc(t, root, "docs", "enterprise-extension-boundaries.md")
	for _, needle := range []string{
		"RequestRegistrations",
		"AttemptRegistrations",
		"ConcurrencyRegistration",
		"RaterRegistrations",
		"RequestProviders",
		"current major",
		"pkg/lipruntime",
		"lipruntime-options-migration.md",
	} {
		if !strings.Contains(enterprise, needle) {
			t.Fatalf("enterprise-extension-boundaries.md missing %q", needle)
		}
	}

	flow := readDoc(t, root, "docs", "runtime-flow.md")
	for _, needle := range []string{
		"lipruntime.Build",
		"legacy",
		"registration",
		"BuildHost",
	} {
		if !strings.Contains(flow, needle) {
			t.Fatalf("runtime-flow.md missing %q", needle)
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
		"RequestProviders:",
		"AttemptProviders:",
		"ConcurrencyProvider:",
		"ProviderDescriptors:",
		"\tRater:",
		"legacy-production-rater",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("enterprise_module must not use legacy field/path %q", forbidden)
		}
	}
	if strings.Contains(src, "internal/") {
		t.Fatal("enterprise_module must not import internal/")
	}
}

func readDoc(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// assertNoInternalImportExamples ensures external guidance does not show
// import ".../internal/..." as a supported path (mentions that forbid internal/
// imports are allowed).
func assertNoInternalImportExamples(t *testing.T, text string) {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*(import\s+)?["\x60][^"\x60]*internal/[^"\x60]+["\x60]`)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "must not") || strings.Contains(lower, "must not import") ||
			strings.Contains(lower, "forbidden") || strings.Contains(lower, "do not import") ||
			strings.Contains(lower, "not import") {
			continue
		}
		if re.MatchString(trimmed) {
			t.Fatalf("migration guide must not present internal/ import examples as supported: %s", trimmed)
		}
	}
}
