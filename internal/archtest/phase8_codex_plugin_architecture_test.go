package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

var phase8MigratedCodexKinds = []string{"openai-codex", "openai-codex-app-server"}

func TestPhase8_migratedCodexAbsentFromEssentialAndMigration(t *testing.T) {
	t.Parallel()
	present := map[string]bool{}
	for _, e := range standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, e := range standardplugins.StandardBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, kind := range phase8MigratedCodexKinds {
		if present[kind] {
			t.Fatalf("migrated kind %q must not appear in essential/standard static bundles", kind)
		}
		if standardplugins.IsEssentialBackendKind(kind) {
			t.Fatalf("migrated kind %q must not be essential", kind)
		}
	}
}

func TestPhase8_internalCodexPackagesRemoved(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, p := range []string{
		filepath.Join("internal", "plugins", "backends", "openaicodex"),
		filepath.Join("internal", "plugins", "backends", "codexappserver"),
		filepath.Join("internal", "core", "codexcatalog"),
	} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Fatalf("%s must be deleted after Phase 8.2 cutover", p)
		}
	}
}

func TestPhase8_externalCodexConnectorPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join("connectors", "codex", "go.mod"),
		filepath.Join("connectors", "codex", "release.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
}

func TestPhase8_MigrationDepsExcludeCodexCatalog(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "pluginreg", "migration_deps.go")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("migration_deps.go must be deleted after Phase 8.4")
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "pluginreg", "generic_host_deps.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{"CodexModelCatalog", "codexcatalog", "CodexModelCatalogSource"} {
		if strings.Contains(text, bad) {
			t.Fatalf("generic_host_deps.go must not contain %q after Phase 8.4", bad)
		}
	}
}

func TestPhase8_UpstreamAPIKeysExcludeOpenAICodex(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal", "standardplugins", "keys.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{"OpenAICodex", "OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_API_KEY", "collectOpenAICodexEnvKeys"} {
		if strings.Contains(text, bad) {
			t.Fatalf("keys.go must not contain %q after Phase 8.2 cutover", bad)
		}
	}
}

func TestCodex_connectorHasNoInternalImports(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connectors", "codex")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("connectors/codex missing: %v", err)
	}
	const rootInternal = "github.com/matdev83/go-llm-interactive-proxy/internal/"
	var walk func(string) error
	walk = func(path string) error {
		infos, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range infos {
			p := filepath.Join(path, e.Name())
			if e.IsDir() {
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), rootInternal) {
				t.Fatalf("%s imports root internal/", p)
			}
		}
		return nil
	}
	if err := walk(dir); err != nil {
		t.Fatal(err)
	}
}
