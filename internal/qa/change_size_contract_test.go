package qa

import (
	"strings"
	"testing"
)

func TestChangeSize_LimitAndOverrideContracts(t *testing.T) {
	t.Parallel()

	policy := readRepositoryFile(t, "tools", "changesize", "policy.go")
	for _, needle := range []string{
		"DefaultLimit      = 100",
		`OverrideEnv       = "LIP_ALLOW_LARGE_CHANGE"`,
		`OverrideGitConfig = "lip.allowLargeChange"`,
	} {
		if !strings.Contains(policy, needle) {
			t.Errorf("tools/changesize/policy.go missing %q", needle)
		}
	}

	agents := readRepositoryFile(t, "AGENTS.md")
	for _, needle := range []string{
		"100 changed files",
		"LIP_ALLOW_LARGE_CHANGE=1",
		"lip.allowLargeChange true",
		"allow-large-change",
		"100 dirty `*.go` files (no override)",
	} {
		if !strings.Contains(agents, needle) {
			t.Errorf("AGENTS.md missing change-size contract %q", needle)
		}
	}

	legacyHook := readRepositoryFile(t, ".githooks", "pre-commit")
	if !strings.Contains(legacyHook, `scripts/check-change-size.sh" --staged`) {
		t.Error(".githooks/pre-commit must run check-change-size.sh --staged")
	}
	manifestHook := readRepositoryFile(t, "scripts", "hooks", "pre-commit")
	if !strings.Contains(manifestHook, `scripts/check-change-size.sh" --staged`) &&
		!strings.Contains(manifestHook, "scripts/check-change-size.sh --staged") {
		t.Error("scripts/hooks/pre-commit must run check-change-size.sh --staged")
	}
	prePush := readRepositoryFile(t, "scripts", "hooks", "pre-push")
	if !strings.Contains(prePush, "scripts/check-change-size.sh") || !strings.Contains(prePush, "--base") {
		t.Error("scripts/hooks/pre-push must run check-change-size.sh --base/--head")
	}

	ci := readRepositoryFile(t, ".github", "workflows", "ci.yml")
	for _, needle := range []string{
		"go run ./tools/changesize --base \"$BASE_SHA\" --head HEAD",
		"allow-large-change",
		"LIP_ALLOW_LARGE_CHANGE",
	} {
		if !strings.Contains(ci, needle) {
			t.Errorf("ci.yml missing change-size contract %q", needle)
		}
	}

	hygiene := readRepositoryFile(t, "internal", "qa", "dirty_go.go")
	if !strings.Contains(hygiene, "maxDirtyGoFiles = 100") {
		t.Error("internal/qa/dirty_go.go must keep maxDirtyGoFiles = 100")
	}
}
