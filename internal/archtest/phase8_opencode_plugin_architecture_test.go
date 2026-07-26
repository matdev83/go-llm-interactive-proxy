package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

var phase8MigratedOpenCodeKinds = []string{"opencode-go", "opencode-zen"}

func TestPhase8_migratedOpenCodeAbsentFromEssentialAndMigration(t *testing.T) {
	t.Parallel()
	present := map[string]bool{}
	for _, e := range standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, e := range standardplugins.StandardBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, kind := range phase8MigratedOpenCodeKinds {
		if present[kind] {
			t.Fatalf("migrated kind %q must not appear in essential/standard static bundles", kind)
		}
		if standardplugins.IsEssentialBackendKind(kind) {
			t.Fatalf("migrated kind %q must not be essential", kind)
		}
	}
}

func TestPhase8_internalOpenCodeBackendPackagesRemoved(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"opencodecommon", "opencodego", "opencodezen"} {
		if _, err := os.Stat(filepath.Join(root, "internal", "plugins", "backends", name)); err == nil {
			t.Fatalf("internal/plugins/backends/%s must be deleted after Phase 8.1 cutover", name)
		}
	}
}

func TestPhase8_externalOpenCodeConnectorPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mod := filepath.Join(root, "connectors", "opencode", "go.mod")
	if _, err := os.Stat(mod); err != nil {
		t.Fatalf("missing %s: %v", mod, err)
	}
	rel := filepath.Join(root, "connectors", "opencode", "release.yaml")
	if _, err := os.Stat(rel); err != nil {
		t.Fatalf("missing %s: %v", rel, err)
	}
}

func TestPhase8_MigrationDepsExcludeOpenCodeResolver(t *testing.T) {
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
	if strings.Contains(text, "ModelVendorResolver") {
		t.Fatalf("generic host deps must not reference ModelVendorResolver: %s", text)
	}
}

func TestPhase8_UpstreamAPIKeysExcludeOpenCode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal", "standardplugins", "keys.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{"OpenCodeGo", "OpenCodeZen", "OPENCODE_GO_API_KEY", "OPENCODE_ZEN_API_KEY", "collectOpenCodeZenEnvKeys"} {
		if strings.Contains(text, bad) {
			t.Fatalf("keys.go must not contain %q after Phase 8.1 cutover", bad)
		}
	}
}

func TestOpenCode_connectorHasNoInternalImports(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connectors", "opencode")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
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
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "github.com/matdev83/go-llm-interactive-proxy/internal/") {
				t.Fatalf("%s imports internal/", p)
			}
		}
		return nil
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := walk(filepath.Join(dir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOpenCode_supportHasNoProviderCouplingInConnector(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connectors", "opencode", "internal", "service")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"openrouter", "nvidia", "huggingface", "ollama", "internal/core/", "internal/plugins/"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(b))
		for _, f := range forbidden {
			if strings.Contains(lower, strings.ToLower(f)) {
				t.Fatalf("%s contains forbidden %q", e.Name(), f)
			}
		}
	}
}
