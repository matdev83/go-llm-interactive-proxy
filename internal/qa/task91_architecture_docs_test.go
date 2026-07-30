package qa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Task 9.1: operator/architecture docs must name the converged runtime ownership
// model (requirements 12.7–12.8) and must not advertise deleted compatibility paths.

func TestDocs_Architecture_OneRuntimeOwnershipContract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	requiredPhrases := []string{
		"one process runtime",
		"one generation runtime",
		"one host",
		"one reload contract",
	}
	architecture := readDoc(t, root, "docs", "architecture.md")
	for _, phrase := range requiredPhrases {
		if !strings.Contains(strings.ToLower(architecture), strings.ToLower(phrase)) {
			t.Fatalf("docs/architecture.md missing ownership phrase %q", phrase)
		}
	}

	anchors := map[string][]string{
		"docs/architecture.md": {
			"runtimebundle.BuildHost",
			"Host.Close",
			"pkg/lipsdk/configreload",
			"GenerationRuntime",
		},
		"docs/runtime-flow.md": {
			"runtimebundle.BuildHost",
			"Host",
			"generation",
			"pkg/lipsdk/configreload",
		},
		"docs/runtime-config-reload.md": {
			"pkg/lipsdk/configreload",
			"Host.Close",
			"explicit",
			"no file watcher",
		},
		"docs/enterprise-extension-boundaries.md": {
			"runtimebundle.BuildHost",
			"RequestRegistrations",
			"pkg/lipruntime",
		},
		"docs/release-gates.md": {
			"one process runtime",
			"one generation runtime",
			"one host",
			"one reload contract",
			"pkg/lipsdk/configreload",
		},
		".kiro/steering/structure.md": {
			"runtimebundle.BuildHost",
			"GenerationRuntime",
			"pkg/lipsdk/configreload",
			"Host.Close",
		},
	}
	for rel, needles := range anchors {
		parts := strings.Split(rel, "/")
		text := readDoc(t, root, parts...)
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s missing required wording %q", rel, needle)
			}
		}
	}
}

func TestDocs_Architecture_NoStaleCompatibilityPaths(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	guidePath := filepath.Join(root, "docs", "lipruntime-options-migration.md")
	if _, err := os.Stat(guidePath); err == nil {
		t.Fatal("docs/lipruntime-options-migration.md must stay deleted")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat migration guide: %v", err)
	}

	// Current-path claims that must not appear as live operator guidance.
	// Historical "superseded/deleted" mentions are allowed only with clear
	// removal language in the same window (see allowRemoval).
	staleCurrent := []string{
		"BuildBootstrap",
		"BootstrapResult",
		"BootstrapMode",
		"AttachReloadHost",
		"RunWithRuntime",
		"requestPlaneAsBuilt",
		"legacy_options.go",
		"lipruntime-options-migration.md",
		"current-major compatibility only",
		"current major source compatibility only",
		"remain source-compatible",
	}
	docs := []string{
		"docs/architecture.md",
		"docs/runtime-flow.md",
		"docs/runtime-config-reload.md",
		"docs/enterprise-extension-boundaries.md",
		"docs/release-gates.md",
		"docs/extension-platform-authoring.md",
		"README.md",
		".kiro/steering/structure.md",
		".kiro/steering/tech.md",
	}
	for _, rel := range docs {
		parts := strings.Split(rel, "/")
		text := readDoc(t, root, parts...)
		lower := strings.ToLower(text)
		for _, claim := range staleCurrent {
			idx := strings.Index(text, claim)
			if idx < 0 {
				idx = strings.Index(lower, strings.ToLower(claim))
			}
			if idx < 0 {
				continue
			}
			windowStart := max(idx-100, 0)
			windowEnd := min(idx+len(claim)+140, len(text))
			window := strings.ToLower(text[windowStart:windowEnd])
			if !allowRemoval(window) {
				t.Fatalf("%s still advertises stale path %q without removal wording: %q",
					rel, claim, text[windowStart:windowEnd])
			}
		}

		// Live composition must not still describe deleted Built / RunWithRuntime products.
		for _, bannedLive := range []string{
			"composes a runnable `Built`",
			"Composes `Built`",
			"runtimebundle.Built",
			"`Run` / `RunWithRuntime`",
			"`Run`/`RunWithRuntime`",
			"assemble `runtimebundle.Built`",
			"-> runtimebundle.Built",
			"Built.DecodeAdmission",
		} {
			if strings.Contains(text, bannedLive) {
				t.Fatalf("%s still describes deleted product path %q", rel, bannedLive)
			}
		}
	}
}

func allowRemoval(window string) bool {
	for _, token := range []string{
		"removed", "deleted", "absent", "no longer", "superseded",
		"must be deleted", "must stay deleted", "retired", "not used",
	} {
		if strings.Contains(window, token) {
			return true
		}
	}
	return false
}
