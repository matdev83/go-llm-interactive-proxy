package qa

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These are lifecycle guarantees, not action SHA pins: an immutable cache
// must advance with source changes and be isolated by toolchain and job.
func TestQAFastPreflight_GoCacheSnapshotsAdvance(t *testing.T) {
	t.Parallel()
	action := readRepositoryFile(t, ".github", "actions", "go-cache", "action.yml")
	for _, needle := range []string{"go env GOVERSION", "RUNNER_OS", "RUNNER_ARCH", "inputs.lane", "hashFiles('**/go.sum')", "github.sha", "restore-keys:", "refs/heads/main", "github.event_name != 'pull_request'", "/cache/download", "scripts/ci-go-cache.py", "--mib 512", "go-mod-v3-shared-", "enableCrossOsArchive: true", "inputs.lane == 'modules'"} {
		if !strings.Contains(action, needle) {
			t.Errorf("shared cache policy missing %q", needle)
		}
	}
	for _, name := range []string{"ci.yml", "qa.yml", "backend-plugin-cross-platform.yml", "acp-process-tree.yml", "cursor-sdk-platform.yml", "development-cost-weekly.yml", "dependency-sync.yml", "connector-pool-race.yml", "openresponses-official-compliance.yml", "openresponses-coverage.yml", "taskrunner-process-tree.yml"} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		if strings.Contains(text, "cache: true") {
			t.Errorf("%s bypasses bounded cache policy", name)
		}
		if !strings.Contains(text, "uses: ./.github/actions/go-cache") || !strings.Contains(text, "phase: save") {
			t.Errorf("%s needs a bounded cache consumer and producer", name)
		}
		if !strings.Contains(text, "push:") && !strings.Contains(text, "schedule:") {
			t.Errorf("%s has no main cache producer", name)
		}
	}
}

func TestQAFastPreflight_DevelopmentCostWatchdog(t *testing.T) {
	t.Parallel()
	workflow := readRepositoryFile(t, ".github", "workflows", "development-cost-weekly.yml")
	var parsed struct {
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatalf("parse paused watchdog: %v", err)
	}
	if parsed.Jobs["windows-cost"].If != "${{ false }}" {
		t.Fatal("remote historical Windows comparison must remain paused")
	}
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
		for line := range strings.SplitSeq(text, "\n") {
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
