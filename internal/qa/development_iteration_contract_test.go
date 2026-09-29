package qa

import (
	"strings"
	"testing"
)

// These are lifecycle guarantees, not action SHA pins: an immutable cache
// must advance with source changes and be isolated by toolchain and job.
func TestQAFastPreflight_GoCacheSnapshotsAdvance(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"ci.yml", "qa.yml", "backend-plugin-cross-platform.yml", "acp-process-tree.yml", "cursor-sdk-platform.yml", "development-cost-weekly.yml"} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		var restores, saves int
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "actions/cache/restore@") {
				restores++
			}
			if strings.Contains(line, "actions/cache/save@") {
				saves++
			}
			if !strings.Contains(line, "key: go-cache-") {
				continue
			}
			for _, needle := range []string{"runner.arch", "outputs.go-version", "github.job", "hashFiles(", "github.sha"} {
				if !strings.Contains(line, needle) {
					t.Errorf("%s cache key must include %s: %s", name, needle, line)
				}
			}
		}
		if restores == 0 || saves != restores {
			t.Errorf("%s has %d restores and %d saves; each job must publish its own progress", name, restores, saves)
		}
		if !strings.Contains(text, `go-version=$(go env GOVERSION)`) {
			t.Errorf("%s must resolve the actual Go version", name)
		}
		if !strings.Contains(text, "restore-keys: |") || !strings.Contains(text, "${{ github.job }}-") {
			t.Errorf("%s must seed new snapshots from the same toolchain/job", name)
		}
	}
}

func TestQAFastPreflight_DevelopmentCostWatchdog(t *testing.T) {
	t.Parallel()
	workflow := readRepositoryFile(t, ".github", "workflows", "development-cost-weekly.yml")
	for _, needle := range []string{"schedule:", "workflow_dispatch:", "windows-latest", "scripts/test-cost-ratchet.ps1", "GOFLAGS: -p=2", "-Parallel 2", "if: always()", "actions/upload-artifact@"} {
		if !strings.Contains(workflow, needle) {
			t.Errorf("cost watchdog missing %q", needle)
		}
	}
	if strings.Contains(workflow, "LIP_ALLOW_TEST_COST_GROWTH") || strings.Contains(workflow, "continue-on-error") {
		t.Fatal("scheduled cost evidence must not override budgets or suppress failures")
	}
}

func TestQAFastPreflight_GoCacheRetentionUsesTrustedCode(t *testing.T) {
	t.Parallel()
	workflow := readRepositoryFile(t, ".github", "workflows", "go-cache-maintenance.yml")
	for _, needle := range []string{"workflow_run:", "branches: [main]", "ref: main", "actions: write", "--paginate --slurp", "state=open", "scripts/prune-go-caches.py", "HTTP 404"} {
		if !strings.Contains(workflow, needle) {
			t.Errorf("cache maintenance missing %q", needle)
		}
	}
	if strings.Contains(workflow, "pull_request:") || strings.Contains(workflow, "download-artifact") {
		t.Fatal("cache write token must never run PR code or consume untrusted artifacts")
	}
	for _, name := range []string{"ci.yml", "qa.yml", "backend-plugin-cross-platform.yml", "acp-process-tree.yml", "cursor-sdk-platform.yml", "development-cost-weekly.yml"} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "name: ") && !strings.Contains(workflow, strings.TrimPrefix(line, "name: ")) {
				t.Errorf("retention trigger omits workflow %s", name)
			}
		}
	}
}

func TestQAFastPreflight_DevelopmentScopeAndLintBudget(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	for _, task := range []string{"test", "build", "lint", "doctor"} {
		if !strings.Contains(makefile, "dev-"+task+":") {
			t.Errorf("missing dev-%s entry point", task)
		}
	}
	for _, name := range []string{"lint-all-modules.sh", "lint-all-modules.ps1"} {
		text := readRepositoryFile(t, "scripts", name)
		for _, needle := range []string{"LIP_LINT_JOBS", "LIP_LINT_CONCURRENCY", "--concurrency="} {
			if !strings.Contains(text, needle) {
				t.Errorf("%s must expose bounded lint scheduling: %s", name, needle)
			}
		}
	}
	agents := readRepositoryFile(t, "AGENTS.md")
	if !strings.Contains(agents, "make dev-test") || !strings.Contains(agents, "make dev-doctor") {
		t.Fatal("agents must be directed to scoped iteration and cache diagnosis")
	}
}
