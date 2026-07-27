package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
)

var phase7MigratedKinds = []string{
	"openrouter", "nvidia", "huggingface",
	"ollama", "ollama-cloud",
	"llamacpp", "lmstudio", "vllm",
}

func TestPhase7_migratedKindsAbsentFromEssentialAndMigration(t *testing.T) {
	t.Parallel()
	present := map[string]bool{}
	for _, e := range standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, e := range standardplugins.StandardBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		present[e.ID] = true
	}
	for _, kind := range phase7MigratedKinds {
		if present[kind] {
			t.Fatalf("migrated kind %q must not appear in essential/standard static bundles", kind)
		}
		if standardplugins.IsEssentialBackendKind(kind) {
			t.Fatalf("migrated kind %q must not be essential", kind)
		}
	}
}

func TestPhase7_internalBackendPackagesRemoved(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"openrouter", "nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm"} {
		if _, err := os.Stat(filepath.Join(root, "internal", "plugins", "backends", name)); err == nil {
			t.Fatalf("internal/plugins/backends/%s must be deleted after Phase 7 cutover", name)
		}
	}
}

func TestPhase7_externalConnectorModulesPresent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"openrouter", "nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm"} {
		mod := filepath.Join(root, "connectors", name, "go.mod")
		if _, err := os.Stat(mod); err != nil {
			t.Fatalf("missing %s: %v", mod, err)
		}
		rel := filepath.Join(root, "connectors", name, "release.yaml")
		if _, err := os.Stat(rel); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	support := filepath.Join(root, "connector-support", "openaicompat", "go.mod")
	b, err := os.ReadFile(support)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "module github.com/matdev83/go-llm-interactive-proxy/connector-support/openaicompat") {
		t.Fatalf("unexpected support module: %s", support)
	}
	if strings.Contains(text, "internal/") {
		t.Fatal("openaicompat support go.mod must not reference internal/")
	}
}

func TestPhase7_DefaultWireModelOmitsMigratedKinds(t *testing.T) {
	t.Parallel()
	for _, kind := range phase7MigratedKinds {
		got := standardplugins.DefaultWireModel(kind)
		if got != "model" {
			t.Fatalf("DefaultWireModel(%q)=%q want generic model", kind, got)
		}
	}
}

func TestPhase7_nonOpenRouterConnectorsOmitOpenRouterKeys(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, name := range []string{"nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm"} {
		dir := filepath.Join(root, "connectors", name, "internal", "service")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "openrouter.") {
				t.Fatalf("%s/%s contains openrouter.* coupling", name, e.Name())
			}
		}
	}
}

func TestOpenRouter_supportHasNoProviderCouplingInSupportModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connector-support", "openaicompat")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"openrouter", "nvidia", "huggingface", "ollama", "llamacpp", "lmstudio", "vllm", "internal/core/", "internal/plugins/"}
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
