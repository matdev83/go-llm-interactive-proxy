package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeepwarmCoreRemainsProviderNeutral(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "internal", "core", "keepwarm")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		source := strings.ToLower(string(data))
		for _, forbidden := range []string{"anthropic", "openai", "gemini", "codex", "lipapi.call", "backend.open", "time.newticker", "prompt_cache_key"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s contains provider/request-routing leakage %q", entry.Name(), forbidden)
			}
		}
	}
}

func TestKeepwarmRuntimeDoesNotReenterNormalExecution(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "internal", "core", "runtime", "keepwarm_integration.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, forbidden := range []string{".Open(", ".Execute(", "route", "failover", "NewCall"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("keep-warm integration contains normal execution/routing token %q", forbidden)
		}
	}
}

func TestKeepwarmManagerHasNoPerTargetTicker(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "internal", "core", "keepwarm", "manager.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "newticker") {
		t.Fatal("keep-warm manager must not allocate a ticker per target")
	}
}
