package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Workflows that pair path-filtered pull_request with push must not also run on
// feature-branch pushes (duplicate with the PR event). Restrict push to main.
func TestWorkflow_pushRestrictedToMainForPRDedup(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	workflows := []string{
		".github/workflows/backend-plugin-cross-platform.yml",
		".github/workflows/backend-plugin-release-gates.yml",
		".github/workflows/codex-connector-race.yml",
	}
	for _, rel := range workflows {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			var doc struct {
				On map[string]any `yaml:"on"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}
			if doc.On == nil {
				t.Fatalf("%s missing on:", rel)
			}
			if _, ok := doc.On["pull_request"]; !ok {
				t.Fatalf("%s must keep pull_request trigger", rel)
			}
			if _, ok := doc.On["workflow_dispatch"]; !ok {
				t.Fatalf("%s must keep workflow_dispatch trigger", rel)
			}
			pushRaw, ok := doc.On["push"]
			if !ok {
				t.Fatalf("%s must keep push trigger", rel)
			}
			push, ok := pushRaw.(map[string]any)
			if !ok {
				t.Fatalf("%s push must be a mapping with branches/paths, got %T", rel, pushRaw)
			}
			branches, ok := stringSliceYAML(push["branches"])
			if !ok || len(branches) != 1 || branches[0] != "main" {
				t.Fatalf("%s push.branches must be exactly [main], got %#v", rel, push["branches"])
			}
			paths, ok := stringSliceYAML(push["paths"])
			if !ok || len(paths) == 0 {
				t.Fatalf("%s push.paths must be preserved non-empty, got %#v", rel, push["paths"])
			}
		})
	}
}

func stringSliceYAML(v any) ([]string, bool) {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case []string:
		return t, true
	default:
		return nil, false
	}
}
