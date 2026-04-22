package qa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedRootMarkdown is the set of markdown files allowed at repository root.
var allowedRootMarkdown = map[string]bool{
	"AGENTS.md": true,
	"README.md": true,
}

func TestRootHygiene_MarkdownFiles(t *testing.T) {
	t.Parallel()

	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}

	var violations []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".md") && !allowedRootMarkdown[name] {
			violations = append(violations, name)
		}
	}
	if len(violations) > 0 {
		t.Errorf("disallowed markdown in repo root: %v (only AGENTS.md, README.md)", violations)
	}
}

func TestLayout_CanonicalInternalDirsExist(t *testing.T) {
	t.Parallel()
	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	dirs := []string{
		"internal/pluginreg",
		"internal/stdhttp",
		"internal/infra/runtimebundle",
		"internal/refbackend",
		"internal/refclient",
		"internal/qa",
		"internal/archtest",
		"internal/plugins/stores",
	}
	for _, rel := range dirs {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			st, err := os.Stat(filepath.Join(rootDir, rel))
			if err != nil {
				t.Fatalf("stat %s: %v", rel, err)
			}
			if !st.IsDir() {
				t.Fatalf("%s: expected directory", rel)
			}
		})
	}
}

func TestRootHygiene_NoLooseTextOrLogFiles(t *testing.T) {
	t.Parallel()

	rootDir, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}

	var bad []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		n := strings.ToLower(entry.Name())
		if strings.HasSuffix(n, ".txt") || strings.HasSuffix(n, ".log") {
			bad = append(bad, entry.Name())
		}
	}
	if len(bad) > 0 {
		t.Errorf("unexpected .txt/.log files in repo root: %v", bad)
	}
}

func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
