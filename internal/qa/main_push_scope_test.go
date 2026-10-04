package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/gitscope"
)

func TestQAFastPreflight_MainPushUsesActualDiff(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", "ci.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	var classifier ciStepSpec
	for _, step := range workflow.Jobs["changes"].Steps {
		if step.ID == "filter" {
			classifier = step
		}
	}
	if classifier.Env["PUSH_BASE_SHA"] != "${{ github.event.before }}" || classifier.Run == "" {
		t.Fatal("main pushes must classify their actual before revision")
	}
	root := t.TempDir()
	git := func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", root, "-c", "user.name=QA", "-c", "user.email=qa@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		// root is a throwaway fixture repository. Git exports GIT_DIR to every hook
		// it runs, so an inherited GIT_DIR would initialise and commit inside the
		// real repository, destroying its index and refs.
		cmd.Env = gitscope.Environ()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(t, "init", "-q")
	script := filepath.Join(root, "scripts", "ci-scope.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(readRepositoryFile(t, "scripts", "ci-scope.sh")), 0o600); err != nil {
		t.Fatal(err)
	}
	// Reuse the repository serially; each scenario still has a real diff.
	scenarios := []struct {
		name, path, before  string
		code, goScope, cost bool
		billing             bool
		invalid             bool
	}{
		{name: "production", path: "internal/example.go", code: true, goScope: true},
		{name: "documentation", path: "docs/example.md"},
		{name: "cost policy", path: "scripts/test-cost-budget.json", code: true, goScope: true, cost: true},
		{name: "billing", path: "internal/core/billing/component_rater.go", code: true, goScope: true, billing: true},
		{name: "shared SDK", path: "pkg/lipsdk/metering/component_key.go", code: true, goScope: true, billing: true},
		{name: "independent oracle", path: "internal/testkit/billsem/solver.go", code: true, goScope: true, billing: true},
		{name: "CI policy", path: ".github/workflows/ci.yml", code: true, goScope: true, billing: true},
		{name: "initial push", path: "docs/example.md", before: strings.Repeat("0", 40), code: true, goScope: true, cost: true, billing: true},
		{name: "invalid predecessor", path: "docs/example.md", before: "missing-revision", invalid: true},
	}
	for _, tc := range scenarios {
		path := filepath.Join(root, filepath.FromSlash(tc.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("base fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, "add", ".")
	git(t, "commit", "-qm", "base")
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			before := "HEAD^"
			path := filepath.Join(root, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("fixture "+tc.name+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			git(t, "commit", "-qam", "head")
			if tc.before != "" {
				before = tc.before
			}
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.Command("bash", "-c", classifier.Run)
			cmd.Dir = root
			// The classifier runs scripts/ci-scope.sh, whose self-test scenarios
			// build throwaway repositories with `git -C "$tmp" init/commit`.
			cmd.Env = append(gitscope.Environ(), "EVENT_NAME=push", "BASE_SHA=", "PUSH_BASE_SHA="+before, "GITHUB_OUTPUT="+output)
			out, err := cmd.CombinedOutput()
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid main predecessor must fail closed")
				}
				return
			}
			if err != nil {
				t.Fatalf("main classifier: %v\n%s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]bool{"code": tc.code, "test": tc.code, "go": tc.goScope, "test_cost": tc.cost, "billing_schema": tc.billing} {
				value := "false"
				if want {
					value = "true"
				}
				if !strings.Contains("\n"+string(data), "\n"+key+"="+value+"\n") {
					t.Fatalf("%s: want %s=%s; got %s", tc.name, key, value, data)
				}
			}
		})
	}
}
