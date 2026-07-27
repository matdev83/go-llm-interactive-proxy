package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase91_SupersedingHybridBackendPluginADR(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "adr", "0008-hybrid-backend-connector-plugins.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Phase 9.1 requires superseding ADR: %v", err)
	}
	body := string(raw)
	lower := strings.ToLower(body)
	for _, want := range []string{
		"hybrid",
		"essential",
		"executable",
		"grpc",
		"native",
		"plugin",
		"manifest",
		"digest",
		"lazy",
		"process model",
		"connectors/",
		"b2bua",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("ADR 0008 missing required topic %q", want)
		}
	}
	if !strings.Contains(body, "Supersedes") && !strings.Contains(body, "supersedes") {
		t.Error("ADR 0008 must declare what it supersedes relative to ADR 0001")
	}
}

func TestPhase91_AuthoritativeSourcesForbidStaticOnlyOptionalTable(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	paths := []string{
		"AGENTS.md",
		".kiro/steering/structure.md",
		".kiro/steering/tech.md",
		".kiro/steering/testing.md",
		"docs/backend-adapter-boundaries.md",
		"docs/architecture.md",
		"EchoesVault/pages/plugin-system.md",
		"EchoesVault/pages/package-map.md",
		"EchoesVault/pages/architecture-overview.md",
		"README.md",
	}
	forbidden := []string{
		"static-only backend",
		"static-only optional",
		"add optional connectors to a fixed table",
		"add an optional connector to standard_table",
		"edit the fixed registration table for optional",
		"internal/plugins/backends/openrouter/",
		"internal/plugins/backends/openaicodex/",
		"internal/core/codexcatalog",
	}
	for _, rel := range paths {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		lower := strings.ToLower(string(raw))
		for _, bad := range forbidden {
			if strings.Contains(lower, strings.ToLower(bad)) || strings.Contains(string(raw), bad) {
				t.Errorf("%s still contains forbidden stale claim %q", rel, bad)
			}
		}
		if !strings.Contains(string(raw), "0008") && !strings.Contains(string(raw), "hybrid") &&
			(strings.Contains(rel, "steering") || strings.Contains(rel, "AGENTS") || strings.Contains(rel, "architecture")) {
			// architecture-overview and AGENTS must mention hybrid or ADR
			if strings.HasSuffix(rel, "AGENTS.md") || strings.Contains(rel, "structure.md") ||
				strings.Contains(rel, "tech.md") || strings.HasSuffix(rel, "architecture.md") ||
				strings.Contains(rel, "architecture-overview.md") {
				if !strings.Contains(lower, "hybrid") && !strings.Contains(string(raw), "0008") &&
					!strings.Contains(lower, "executable") && !strings.Contains(lower, "connectors/") {
					t.Errorf("%s must describe hybrid/external connector composition", rel)
				}
			}
		}
	}
}

func TestPhase91_EchoesVaultIndexListsHybridConnectorPage(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	idx := readFile(t, filepath.Join(root, "EchoesVault", "index.md"))
	if !strings.Contains(idx, "[[backend-connector-plugins]]") {
		t.Fatal("EchoesVault/index.md must list [[backend-connector-plugins]]")
	}
	page := filepath.Join(root, "EchoesVault", "pages", "backend-connector-plugins.md")
	raw, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("missing EchoesVault page: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"type:", "connectors/", "manifest", "essential", "gRPC"} {
		if !strings.Contains(body, want) {
			t.Errorf("backend-connector-plugins.md missing %q", want)
		}
	}
}

func TestPhase91_DocsCheckAndKnowledgeCheckTargets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mk := readFile(t, filepath.Join(root, "Makefile"))
	if !strings.Contains(mk, "docs-check:") {
		t.Fatal("Makefile missing docs-check")
	}
	if !strings.Contains(mk, "knowledge-check:") {
		t.Fatal("Makefile missing knowledge-check")
	}
	if !strings.Contains(mk, "knowledge-check") || !strings.Contains(firstPHONYLine(mk), "knowledge-check") {
		t.Fatal(".PHONY must include knowledge-check")
	}
}
