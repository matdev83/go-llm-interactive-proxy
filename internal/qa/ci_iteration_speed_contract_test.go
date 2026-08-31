package qa

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCIIterationSpeed_ModuleTidyUsesBoundedParallelism(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(repositoryFile(t, "scripts", "tidy-all-modules.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, needle := range []string{
		"LIP_MODULE_CHECK_JOBS",
		"xargs -r -P\"$JOBS\"",
		"go build -o \"$DISCOVER_MODULES_BIN\" ./tools/backendplugin/discover_modules",
		"GOWORK=off go mod tidy",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("tidy-all-modules.sh missing bounded/reused helper contract %q", needle)
		}
	}
}

func TestCIIterationSpeed_LocalMakeGraphKeepsFastAndFullQualityContracts(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	for _, needle := range []string{
		"quality-checks-fast:",
		"test: quality-checks-fast test-unit parity-checks",
		"test-fast: quality-checks-fast",
		"qa: quality-checks-fast qa-tests",
	} {
		if !strings.Contains(makefile, needle) {
			t.Fatalf("Makefile missing local speed/coverage contract %q", needle)
		}
	}
	// The complete cached root graph is the invariant; the parallelism value
	// intentionally tracks the machine (LIP_TEST_PARALLEL/core detection) so it
	// is asserted as present rather than pinned to a fixed count.
	stagedSh := readRepositoryFile(t, "scripts", "test-staged.sh")
	if !strings.Contains(stagedSh, "\"${pre_flags[@]}\" ./...") || !strings.Contains(stagedSh, "-parallel=") {
		t.Fatal("POSIX test-staged route must run the complete cached root graph")
	}
	stagedPs1 := readRepositoryFile(t, "scripts", "test-staged.ps1")
	if !strings.Contains(stagedPs1, "@preFlags ./...") || !strings.Contains(stagedPs1, "-parallel=") {
		t.Fatal("Windows test-staged route must run the complete cached root graph")
	}
	for _, name := range []string{"scripts/quality-checks.sh", "scripts/quality-checks.ps1"} {
		text := readRepositoryFile(t, strings.Split(name, "/")...)
		if !strings.Contains(text, "LIP_SKIP_GO_COMPILE_CHECKS") {
			t.Fatalf("%s must expose the explicit duplicate-build/vet fast-path switch", name)
		}
	}
}

func TestCIIterationSpeed_MatrixScopeProbeAndDedicatedCaches(t *testing.T) {
	t.Parallel()

	probe := readRepositoryFile(t, "scripts", "makefile-scope.sh")
	for _, needle := range []string{
		"--relevant BASE_SHA HEAD_SHA SCOPE",
		"--self-test",
		"acp|cursorcliacp",
		"backend-plugin",
		"cursor-sdk|cursorsdk",
		".PHONY mega-line",
	} {
		if !strings.Contains(probe, needle) {
			t.Fatalf("scripts/makefile-scope.sh missing contract %q", needle)
		}
	}

	// Expensive 3-OS matrices must consult the probe before running so an
	// unrelated Makefile edit cannot burn the matrix.
	for _, name := range []string{
		"backend-plugin-cross-platform.yml",
		"acp-process-tree.yml",
		"cursor-sdk-platform.yml",
	} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		if !strings.Contains(text, "scripts/makefile-scope.sh") {
			t.Errorf("%s does not consult the Makefile relevance probe", name)
		}
	}

	// The shared setup-go cache is a frozen first-write-wins partial snapshot;
	// every workflow that runs heavy Go work must own a dedicated complete cache
	// and must keep the shared setup-go cache disabled (cache: false) so it can
	// never be re-enabled and silently re-freeze the partial snapshot.
	for _, pair := range []struct {
		workflow, key string
	}{
		{"qa.yml", "go-cache-qa-"},
		{"ci.yml", "go-cache-ci-"},
		{"backend-plugin-cross-platform.yml", "go-cache-backend-plugin-"},
		{"acp-process-tree.yml", "go-cache-acp-"},
		{"cursor-sdk-platform.yml", "go-cache-cursorsdk-"},
	} {
		text := readRepositoryFile(t, ".github", "workflows", pair.workflow)
		for _, needle := range []string{
			"actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9",
			"actions/cache/save@55cc8345863c7cc4c66a329aec7e433d2d1c52a9",
			pair.key + "${{ runner.os }}",
			"cache: false",
		} {
			if !strings.Contains(text, needle) {
				t.Errorf("%s missing dedicated cache contract %q", pair.workflow, needle)
			}
		}
	}
}

func TestCIIterationSpeed_WorkflowConcurrencyAndCaches(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"ci.yml",
		"qa.yml",
		"security.yml",
		"release.yml",
		"race-fuzz-nightly.yml",
		"codeql.yml",
		"openresponses-coverage.yml",
	} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		if !strings.Contains(text, "github.head_ref || github.ref_name") && name != "release.yml" && name != "race-fuzz-nightly.yml" {
			t.Errorf("%s does not share branch/PR concurrency identity", name)
		}
	}

	qa := readRepositoryFile(t, ".github", "workflows", "qa.yml")
	if !strings.Contains(qa, "CI owns the portable cmd/lipstd test/build matrix") {
		t.Fatal("QA ownership documentation is missing")
	}
	preflight := strings.Index(qa, "- name: Fast policy preflight")
	fixtureTidy := strings.Index(qa, "- name: Fixture module tidy preflight")
	profile := strings.Index(qa, "- name: Provider-profile change-surface ratchet")
	vet := strings.Index(qa, "- name: Vet release command")
	architecture := strings.Index(qa, "- name: Architecture guardrails")
	if preflight < 0 || fixtureTidy < 0 || profile < 0 || vet < 0 || architecture < 0 ||
		preflight >= fixtureTidy || fixtureTidy >= profile || profile >= vet || vet >= architecture {
		t.Error("QA must order policy, fixture tidy, profile, and vet gates before heavy architecture guardrails")
	}
	if preflight >= 0 && fixtureTidy > preflight {
		preflightStep := strings.Join(strings.Fields(qa[preflight:fixtureTidy]), " ")
		for _, needle := range []string{
			"go test",
			"-count=1",
			"-v",
			"-tags=precommit",
			"./internal/qa",
			"TestRootHygiene_",
			"TestQAFastPreflight_",
			"tee",
			"grep -qE",
		} {
			if !strings.Contains(preflightStep, needle) {
				t.Errorf("QA fast policy preflight missing %q", needle)
			}
		}
	}
	normalizedQA := strings.Join(strings.Fields(qa), " ")
	for _, needle := range []string{
		"testdata/enterprise_module testdata/external_connector",
		"GOWORK=off go mod tidy -diff",
		"id: archtest",
		"contains(fromJSON('[\"success\",\"failure\"]'), steps.archtest.outcome)",
	} {
		if !strings.Contains(normalizedQA, needle) {
			t.Errorf("QA fast-preflight contract missing %q", needle)
		}
	}
	cacheKey := "hashFiles('go.sum', 'testdata/enterprise_module/go.sum', 'testdata/external_connector/go.sum')"
	if count := strings.Count(normalizedQA, cacheKey); count != 2 {
		t.Errorf("QA dedicated cache key occurs %d times, want restore and save keys", count)
	}
	if strings.Contains(qa, "go test -timeout=5m ./cmd/lipstd") {
		t.Fatal("QA must not duplicate the CI cmd/lipstd test")
	}
	ci := readRepositoryFile(t, ".github", "workflows", "ci.yml")
	for _, needle := range []string{"go test -timeout=8m ${{ matrix.packages }}", "go build -trimpath ./cmd/lipstd"} {
		if !strings.Contains(ci, needle) {
			t.Fatalf("CI no longer owns portable cmd/lipstd evidence %q", needle)
		}
	}

	for _, name := range []string{
		"security.yml",
		"release.yml",
		"race-fuzz-nightly.yml",
		"optional-gosec.yml",
		"benchmarks.yml",
		"modernize-monthly.yml",
		"reasoning-e2e-soak-nightly.yml",
	} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		for _, needle := range []string{
			"connectors/**/go.sum",
			"connector-support/**/go.sum",
			"testdata/enterprise_module/go.sum",
			"tools/**/go.sum",
		} {
			if !strings.Contains(text, needle) {
				t.Errorf("%s cache-dependency-path missing %q", name, needle)
			}
		}
	}
}

func TestQAFastPreflight_TestCostRatchetContracts(t *testing.T) {
	t.Parallel()

	makefile := readRepositoryFile(t, "Makefile")
	if !strings.Contains(strings.SplitN(makefile, "\n", 2)[0], "test-cost") {
		t.Fatal("Makefile .PHONY declaration must include test-cost")
	}
	for _, needle := range []string{
		"TEST_COST_BASE_SHA ?=",
		"TEST_COST_OUTPUT_ROOT ?=",
		"TEST_COST_PARALLEL ?= 0",
		"make test-cost",
		"Windows-authoritative",
	} {
		if !strings.Contains(makefile, needle) {
			t.Fatalf("Makefile missing test-cost interface/help contract %q", needle)
		}
	}
	target := makeTargetBlock(makefile, "test-cost")
	for _, needle := range []string{
		"test-cost:",
		"ifeq ($(OS),Windows_NT)",
		"scripts/test-cost-ratchet.ps1",
		"-BaseSHA",
		"-OutputRoot",
		"-Parallel",
		"Windows-only",
		"exit 1",
	} {
		if !strings.Contains(target, needle) {
			t.Fatalf("test-cost target missing contract %q", needle)
		}
	}
	if strings.Contains(makeTargetBlock(makefile, "test"), "test-cost") {
		t.Fatal("test-cost must remain opt-in, not a make test prerequisite")
	}

	script := readRepositoryFile(t, "scripts", "test-cost-ratchet.ps1")
	for _, needle := range []string{
		"function Test-IsWindows",
		"if (-not (Test-IsWindows))",
		"$Targets = @(\"test-unit\", \"quality-checks\")",
		"-count=1",
		"LIP_ALLOW_TEST_COST_GROWTH",
		"worktree\", \"add\", \"--detach\"",
		"worktree\", \"remove\", \"--force\"",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("test-cost-ratchet script missing contract %q", needle)
		}
	}
	if !strings.Contains(script, `"-c", "core.autocrlf=false"`) {
		t.Fatal("anchor worktree creation must disable autocrlf for the command")
	}
	if !strings.Contains(script, `"-c", "core.eol=lf"`) {
		t.Fatal("anchor worktree creation must force LF checkout for the command")
	}
	if strings.Contains(script, `"add", "-A"`) || strings.Contains(script, `"add", "."`) {
		t.Fatal("anchor compatibility commit must not stage unrelated checkout conversions")
	}
	for _, compatibilityPath := range []string{
		"internal/testkit/dbparity/cmd/main_test.go",
		"internal/testkit/postgres_makefile_gate_test.go",
		"internal/stdhttp/security_guard.go",
		"internal/infra/backendplugins/processhost/windows_production_test.go",
		"internal/testkit/backendplugin/cmd/lip-backendplugin-fake/pipe_windows.go",
		"scripts/taskrunner.ps1",
		"internal/qa/windows_task_reliability_contract_test.go",
		"tools/openresponses_compliance/src/lib/compliance-tests.ts",
		"internal/archtest/extension_planes_baseline.json",
	} {
		if !strings.Contains(script, compatibilityPath) {
			t.Fatalf("anchor compatibility paths must remain explicit: %q", compatibilityPath)
		}
	}

	var policy struct {
		SchemaVersion int                        `json:"schema_version"`
		AnchorRef     string                     `json:"anchor_ref"`
		Targets       map[string]json.RawMessage `json:"targets"`
	}
	policyBytes := []byte(readRepositoryFile(t, "scripts", "test-cost-budget.json"))
	if err := json.Unmarshal(policyBytes, &policy); err != nil {
		t.Fatalf("test-cost policy must be valid JSON: %v", err)
	}
	if policy.SchemaVersion != 1 || strings.TrimSpace(policy.AnchorRef) == "" {
		t.Fatalf("test-cost policy must declare schema_version=1 and a non-empty anchor_ref: %#v", policy)
	}
	for _, targetName := range []string{"test-unit", "quality-checks"} {
		if _, ok := policy.Targets[targetName]; !ok {
			t.Fatalf("test-cost policy missing authoritative target %q", targetName)
		}
	}

	ci := readRepositoryFile(t, ".github", "workflows", "ci.yml")
	for _, needle := range []string{
		"types: [opened, synchronize, reopened, labeled, unlabeled]",
		"github.event.pull_request.base.sha",
		"github.head_ref || github.ref_name",
		"- os: ubuntu-latest",
		"- os: windows-latest",
		"- os: macos-latest",
		"go test -timeout=8m ${{ matrix.packages }}",
		"- name: Windows test-cost ratchet",
		"matrix.os == 'windows-latest'",
		"allow-test-cost-growth",
		"-BaseSHA \"${{ github.event.pull_request.base.sha }}\"",
	} {
		if !strings.Contains(ci, needle) {
			t.Fatalf("CI workflow missing test-cost/iteration-speed contract %q", needle)
		}
	}
	fastUnit := strings.Index(ci, "- name: Fast unit tests")
	ratchet := strings.Index(ci, "- name: Windows test-cost ratchet")
	if fastUnit < 0 || ratchet < 0 || fastUnit >= ratchet {
		t.Fatal("CI must place the portable fast-unit step before the Windows authoritative ratchet")
	}
	fastUnitBlock := ci[fastUnit:ratchet]
	if strings.Contains(fastUnitBlock, "continue-on-error") || strings.Contains(fastUnitBlock, "runner.os !=") {
		t.Fatal("CI must not bypass the portable fast-unit contract on Windows through a soft-failure condition")
	}
}
