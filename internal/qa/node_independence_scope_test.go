package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const nodeIndependenceWorkflow = "node-independence.yml"

// The No-Node lane costs about 7 minutes. It guards one property: the root build
// must never need Node. Module/packaging inputs and the guard's own files always
// run it; edits to the Makefile, scripts and tools run it only when the changed
// lines mention a Node toolchain, so a one-line unrelated edit no longer costs
// every PR the lane (the daily schedule still replays it).
func TestNodeIndependenceScope_ContentJudgedFiles(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", nodeIndependenceWorkflow)), &workflow); err != nil {
		t.Fatal(err)
	}
	var classify ciStepSpec
	for _, step := range workflow.Jobs["changes"].Steps {
		if step.ID == "filter" {
			classify = step
		}
	}
	if classify.Run == "" {
		t.Fatal("node-independence workflow lost its filter step")
	}

	baseline := newQAGitFixture(t)
	for path, text := range map[string]string{
		"Makefile":                           ".PHONY: help\nhelp:\n\t@echo usage\n",
		"scripts/helper.sh":                  "#!/usr/bin/env bash\necho helper\n",
		"tools/devcheck/main.go":             "package main\n",
		"internal/core/example.go":           "package core\n",
		"docs/example.md":                    "doc\n",
		"scripts/check-node-independence.sh": "#!/usr/bin/env bash\necho guard\n",
		"go.mod":                             "module example\n",
	} {
		baseline.write(t, path, text)
	}
	baseline.git(t, "add", ".")
	baseline.git(t, "commit", "-qm", "base")

	for _, tc := range []struct {
		name, path, appended string
		want                 string
	}{
		{"unrelated makefile target", "Makefile", "lint:\n\t@echo lint\n", "false"},
		{"unrelated script edit", "scripts/helper.sh", "echo more\n", "false"},
		{"unrelated tool edit", "tools/devcheck/main.go", "// comment\n", "false"},
		{"go source edit", "internal/core/example.go", "// comment\n", "false"},
		{"docs edit", "docs/example.md", "more\n", "false"},
		{"makefile adds npm", "Makefile", "docs-site:\n\tnpm run build\n", "true"},
		{"script adds npx", "scripts/helper.sh", "npx tsc\n", "true"},
		{"tool references node", "tools/devcheck/main.go", "// uses node\n", "true"},
		{"guard script edit", "scripts/check-node-independence.sh", "echo changed\n", "true"},
		{"module file edit", "go.mod", "// dep\n", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := baseline.cloneInto(t)
			before := strings.TrimSpace(fixture.git(t, "rev-parse", "HEAD"))
			current, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(tc.path)))
			if err != nil {
				t.Fatal(err)
			}
			fixture.write(t, tc.path, string(current)+tc.appended)
			fixture.git(t, "commit", "-qam", "change")
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.CommandContext(t.Context(), "bash", "-c", classify.Run)
			cmd.Dir = fixture.root
			cmd.Env = fixture.commandEnv("EVENT_NAME=push", "BASE_SHA="+before, "GITHUB_OUTPUT="+output)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("classifier: %v\n%s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains("\n"+string(data), "\nhost="+tc.want+"\n") {
				t.Fatalf("want host=%s, got %s", tc.want, data)
			}
		})
	}
}
