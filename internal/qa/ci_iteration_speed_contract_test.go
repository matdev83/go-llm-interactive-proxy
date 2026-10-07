package qa

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/tools/testcost"
	"gopkg.in/yaml.v3"
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
	qualityPS1 := readRepositoryFile(t, "scripts", "quality-checks.ps1")
	if !strings.Contains(qualityPS1, `@("go", "mod", "tidy", "-diff")`) {
		t.Fatal("Windows quality checks must verify module tidiness without writing tracked files")
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

	// Every heavy lane uses the shared bounded policy; cache identity and
	// publication rules are owned once by the composite action.
	for _, name := range []string{"qa.yml", "ci.yml", "backend-plugin-cross-platform.yml", "acp-process-tree.yml", "cursor-sdk-platform.yml"} {
		text := readRepositoryFile(t, ".github", "workflows", name)
		for _, needle := range []string{"uses: ./.github/actions/go-cache", "phase: save", "cache: false"} {
			if !strings.Contains(text, needle) {
				t.Errorf("%s missing cache contract %q", name, needle)
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
		"testdata/enterprise_module testdata/external_connector testdata/external_feature_sdk",
		"GOWORK=off go mod tidy -diff",
		"id: archtest",
	} {
		if !strings.Contains(normalizedQA, needle) {
			t.Errorf("QA fast-preflight contract missing %q", needle)
		}
	}

	if strings.Contains(qa, "go test -timeout=5m ./cmd/lipstd") {
		t.Fatal("QA must not duplicate the CI cmd/lipstd test")
	}
	ci := readRepositoryFile(t, ".github", "workflows", "ci.yml")
	for _, needle := range []string{"go test -timeout=8m ${{ matrix.packages }}", "go build ./cmd/lipstd"} {
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
		if !strings.Contains(text, "uses: ./.github/actions/go-cache") {
			t.Errorf("%s must use the shared all-module dependency fingerprint", name)
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
		"$Targets = @(\"test-unit\", \"quality-checks\", \"qa-tagged-hotspots\")",
		`@("mod", "download", "all")`,
		`Invoke-RequiredExternal "$Label-hotspots" "go" @("test", "-run", '^$', "-count=1", "-parallel=$TestParallel", "-timeout=10m", "-tags=precommit,integration", "./internal/archtest", "./internal/infra/runtimebundle", "./tools/backendplugin/...") $TreeRoot $TempRoot`,
		`Invoke-RequiredExternal "anchor-compatibility-tidy" "go" @("mod", "tidy")`,
		`throw "anchor compatibility go mod tidy unexpectedly changed go.mod"`,
		`@("build", "-buildvcs=false", "-o", $warmBinary, "./cmd/lipstd")`,
		`SetEnvironmentVariable("GIT_CONFIG_COUNT", "2", "Process")`,
		"LIP_QA_LIPSTD_BINARY",
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
	if !strings.Contains(script, "$content.Replace(\"`r`n\", \"`n\")") {
		t.Fatal("anchor compatibility files must be LF-normalized before quality-check measurement")
	}
	for _, shortTempRoot := range []string{`("a-" + $runID.Substring(0, 8))`, `("h-" + $runID.Substring(0, 8))`} {
		if !strings.Contains(script, shortTempRoot) {
			t.Fatalf("isolated measurement temp roots must stay short enough for Windows image paths: %q", shortTempRoot)
		}
	}
	compatibilityStart := strings.Index(script, "$compatibilityPaths = @(")
	if compatibilityStart < 0 {
		t.Fatal("anchor compatibility paths must be declared explicitly")
	}
	if firstUse := strings.Index(script, "$compatibilityPaths"); firstUse != compatibilityStart {
		t.Fatal("anchor compatibility paths must be declared before StrictMode can observe a use")
	}
	compatibilityEnd := strings.Index(script[compatibilityStart:], ")")
	if compatibilityEnd < 0 {
		t.Fatal("anchor compatibility path declaration is unterminated")
	}
	compatibilityBlock := script[compatibilityStart : compatibilityStart+compatibilityEnd]
	for _, compatibilityPath := range []string{
		"internal/testkit/dbparity/cmd/main_test.go",
		"internal/testkit/postgres_makefile_gate_test.go",
	} {
		if !strings.Contains(compatibilityBlock, compatibilityPath) {
			t.Fatalf("anchor compatibility paths must remain explicit: %q", compatibilityPath)
		}
	}
	for _, loadCompatibilityPath := range []string{
		"internal/stdhttp/request_plane_generation_test.go",
		"internal/infra/runtimebundle/publish_pinned_characterization_test.go",
		"internal/infra/runtimehost/observability_test.go",
		"internal/qa/phase74_migration_rollout_evidence_test.go",
		"internal/plugins/frontends/openresponses/websocket_upgrade_test.go",
		"scripts/quality-checks.ps1",
		"tools/changesize/main_test.go",
	} {
		if !strings.Contains(script, loadCompatibilityPath) {
			t.Fatalf("anchor load compatibility must remain test-only and explicit: %q", loadCompatibilityPath)
		}
	}
	currentAnchorStart := strings.Index(script, "$testCompatibilityPathsByAnchor = @{")
	if currentAnchorStart < 0 {
		t.Fatal("current-anchor test compatibility must declare an explicit per-anchor map")
	}
	currentAnchorEnd := strings.Index(script[currentAnchorStart:], "}")
	if currentAnchorEnd < 0 {
		t.Fatal("current-anchor test compatibility map is unterminated")
	}
	currentAnchorBlock := script[currentAnchorStart : currentAnchorStart+currentAnchorEnd]
	for _, currentAnchorPath := range []string{
		"6dbb831885341516117034923f0c3203373aded0",
		"internal/core/runtime/parallel_race_late_arm_race_test.go",
		"bb1ef9620ee6e8d9199950161e46fc51914945f2",
		"tools/changesize/main_test.go",
		"internal/plugins/frontends/frontendpipe/candidate_proof_saturation_race_test.go",
		"internal/plugins/frontends/frontendpipe/candidate_assessment_saturation_race_test.go",
	} {
		if !strings.Contains(currentAnchorBlock, currentAnchorPath) {
			t.Fatalf("current-anchor test compatibility must keep the active anchor hermetic: %q", currentAnchorPath)
		}
	}
	for _, warmupModuleGuard := range []string{
		`$moduleSnapshots = @{}`,
		`[IO.File]::WriteAllBytes($modulePath, $moduleSnapshots[$modulePath])`,
	} {
		if !strings.Contains(script, warmupModuleGuard) {
			t.Fatalf("test-cost warmup must restore clean module files before measurement: %q", warmupModuleGuard)
		}
	}
	for _, forbiddenPath := range []string{
		"internal/stdhttp/security_guard.go",
		"internal/infra/backendplugins/processhost/windows_production_test.go",
		"internal/testkit/backendplugin/cmd/lip-backendplugin-fake/pipe_windows.go",
		"scripts/taskrunner.ps1",
		"internal/qa/windows_task_reliability_contract_test.go",
		"tools/openresponses_compliance/src/lib/compliance-tests.ts",
		"internal/archtest/extension_planes_baseline.json",
	} {
		if strings.Contains(script, forbiddenPath) {
			t.Fatalf("anchor compatibility must not mutate speculative path %q", forbiddenPath)
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
	for _, targetName := range []string{"test-unit", "quality-checks", "qa-tagged-hotspots"} {
		if _, ok := policy.Targets[targetName]; !ok {
			t.Fatalf("test-cost policy missing authoritative target %q", targetName)
		}
	}

	ci := workflowJob(t, "ci.yml", "test")
	for _, needle := range []string{
		"concurrency:",
		"group: ci-${{ github.head_ref || github.ref_name }}",
		"cancel-in-progress: true",
		"- os: ubuntu-latest",
		"- os: windows-latest",
		"- os: macos-latest",
		"go test -timeout=8m ${{ matrix.packages }}",
		"- name: Build release binary",
	} {
		if !strings.Contains(ci, needle) {
			t.Fatalf("CI workflow missing portable test/build contract %q", needle)
		}
	}
	for _, forbidden := range []string{"scripts/test-cost-ratchet.ps1", "Windows test-cost history", "windows-test-cost"} {
		if strings.Contains(ci, forbidden) {
			t.Fatalf("default CI must not run the paused historical Windows comparison: %q", forbidden)
		}
	}
	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				With struct {
					FetchDepth *int `yaml:"fetch-depth"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(ci), &parsed); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	for _, step := range parsed.Jobs["test"].Steps {
		if depth := step.With.FetchDepth; depth != nil && *depth == 0 {
			t.Fatal("portable test/build jobs must not fetch the frozen comparison history")
		}
	}
	fastUnit := strings.Index(ci, "- name: Fast unit tests")
	buildBinary := strings.Index(ci, "- name: Build release binary")
	if fastUnit < 0 || buildBinary <= fastUnit {
		t.Fatal("CI must run portable unit tests before the release build")
	}
	fastUnitBlock := ci[fastUnit:buildBinary]
	if strings.Contains(fastUnitBlock, "continue-on-error") || strings.Contains(fastUnitBlock, "test_cost") || strings.Contains(fastUnitBlock, "labels") {
		t.Fatal("ordinary Windows unit tests must not be skipped or softened by cost policy changes or labels")
	}
	if !strings.Contains(fastUnitBlock, "if: needs.changes.result == 'success' && env.RUN_LEG == 'true'") {
		t.Fatal("portable units must follow the per-leg RUN_LEG scope on every matrix platform")
	}
	// Linux is the PR gate. Windows and macOS legs run for OS-sensitive paths,
	// the full-ci label, and the daily schedule; the job-level RUN_LEG
	// expression is the single place that policy lives.
	runLeg := ci[strings.Index(ci, "RUN_LEG:"):]
	runLeg = runLeg[:strings.Index(runLeg, "\n")]
	for _, want := range []string{"needs.changes.outputs.test == 'true'", "matrix.os == 'ubuntu-latest'", "needs.changes.outputs.os_sensitive == 'true'", "github.event_name == 'schedule'", "full-ci"} {
		if !strings.Contains(runLeg, want) {
			t.Fatalf("RUN_LEG must keep %q so non-Linux legs stay scheduled/OS-scoped", want)
		}
	}
	if !strings.Contains(ci, "schedule:") {
		t.Fatal("CI must keep its daily schedule so Windows and macOS still run every day")
	}
}

func TestCIIterationSpeed_QATestsCuratedDeltaAndCanonicalContracts(t *testing.T) {
	t.Parallel()

	curatedDeltaPackages := []string{
		"./internal/qa/...",
		"./internal/core/runtime/...",
		"./internal/stdhttp/...",
		"./internal/testkit/conformance/...",
		"./tools/backendplugin/...",
		"./internal/core/billing/...",
	}

	makefile := readRepositoryFile(t, "Makefile")
	qaTarget := makeTargetBlock(makefile, "qa-tests")
	if qaTarget == "" {
		t.Fatal("Makefile missing qa-tests target block")
	}
	if !strings.Contains(qaTarget, "@$(WINDOWS_TASK) qa-tests") {
		t.Fatal("Makefile qa-tests target must delegate to WINDOWS_TASK on Windows")
	}
	if !strings.Contains(qaTarget, "$$LIP_SKIP_QA_TESTS") {
		t.Fatal("Makefile qa-tests POSIX branch must check $$LIP_SKIP_QA_TESTS")
	}
	expectedPOSIXDelta := "-tags=precommit,integration " + strings.Join(curatedDeltaPackages, " ")
	if !strings.Contains(qaTarget, expectedPOSIXDelta) {
		t.Fatalf("Makefile qa-tests skip branch must run curated tagged delta %q", expectedPOSIXDelta)
	}
	if !strings.Contains(qaTarget, "-tags=precommit,integration ./...") {
		t.Fatal("Makefile qa-tests non-skip branch must run canonical ./...")
	}

	winTask := readRepositoryFile(t, "scripts", "windows-task.ps1")
	for _, envVar := range []string{
		"LIP_TEST_POSTGRES_DSN=",
		"LIP_TEST_POSTGRES_ADMIN_DSN=",
		"LIP_MANAGED_POSTGRES_DSN=",
		"LIP_MIGRATION_POSTGRES_DSN=",
	} {
		if !strings.Contains(winTask, envVar) {
			t.Fatalf("scripts/windows-task.ps1 qa-tests must clear %s", envVar)
		}
	}
	if !strings.Contains(winTask, "$env:LIP_SKIP_QA_TESTS") {
		t.Fatal("scripts/windows-task.ps1 qa-tests must check $env:LIP_SKIP_QA_TESTS")
	}

	quotedDeltaPkgs := make([]string, len(curatedDeltaPackages))
	for i, p := range curatedDeltaPackages {
		quotedDeltaPkgs[i] = `"` + p + `"`
	}
	expectedWinDelta := `@("-tags=precommit,integration", ` + strings.Join(quotedDeltaPkgs, ", ") + `)`
	if !strings.Contains(winTask, expectedWinDelta) {
		t.Fatalf("scripts/windows-task.ps1 qa-tests:precommit-delta must match curated tagged delta %q", expectedWinDelta)
	}
	expectedWinCanonical := `@("-tags=precommit,integration", "./...")`
	if !strings.Contains(winTask, expectedWinCanonical) {
		t.Fatalf("scripts/windows-task.ps1 qa-tests:root must run canonical %q", expectedWinCanonical)
	}
}

// TestCIIterationSpeed_QATestsBillingBudgetIsCrossPlatform pins that the widened
// qa-tests budget reaches BOTH host families. It was introduced for POSIX only,
// which left the same exhaustive billing sweep failing on Windows purely on host
// speed: a portability difference that says nothing about correctness and can
// never be diagnosed from the failing side.
func TestCIIterationSpeed_QATestsBillingBudgetIsCrossPlatform(t *testing.T) {
	t.Parallel()

	makefile := readRepositoryFile(t, "Makefile")
	winTask := readRepositoryFile(t, "scripts", "windows-task.ps1")

	if !strings.Contains(makefile, "BILLING_SCHEMA_TIMEOUT ?= 30m") {
		t.Fatal("Makefile must keep one shared BILLING_SCHEMA_TIMEOUT budget")
	}
	if !strings.Contains(makefile, "QA_TESTS_GO_TEST_FLAGS = $(filter-out -timeout=%,$(GO_TEST_FLAGS)) -timeout=$(BILLING_SCHEMA_TIMEOUT)") {
		t.Fatal("Makefile POSIX qa-tests must replace only the -timeout value and keep every other flag")
	}

	// The Windows orchestrator must resolve the same variable and must not hand
	// the raw default 10m flag list to either qa-tests invocation.
	if !strings.Contains(winTask, "BILLING_SCHEMA_TIMEOUT") {
		t.Fatal("scripts/windows-task.ps1 must resolve the shared BILLING_SCHEMA_TIMEOUT budget")
	}
	if !strings.Contains(winTask, "Get-QaTestsGoTestFlags") {
		t.Fatal("scripts/windows-task.ps1 must rebuild the qa-tests flag list around the billing budget")
	}
	if !strings.Contains(winTask, "Get-QaTestsTaskTimeout") {
		t.Fatal("scripts/windows-task.ps1 must derive the supervisor timeout from the billing budget")
	}
	if !strings.Contains(winTask, "$innerMinutes + 5") {
		t.Fatal("scripts/windows-task.ps1 must keep a shutdown margin between the Go and supervisor timeouts")
	}
	// Scope the assertion to the qa-tests switch arm: other targets legitimately
	// keep the default budget.
	qaArm := winTask[strings.Index(winTask, `"^qa-tests$"`):]
	if end := strings.Index(qaArm, `"^test-fuzz$"`); end > 0 {
		qaArm = qaArm[:end]
	}
	if strings.Contains(qaArm, "@($goTestFlags) +") {
		t.Fatal("scripts/windows-task.ps1 qa-tests must not pass the unbudgeted $goTestFlags; " +
			"the billing sweep would fail on Windows on host speed alone")
	}
	if !strings.Contains(qaArm, "@($qaTestFlags) +") {
		t.Fatal("scripts/windows-task.ps1 qa-tests must invoke the budgeted flag list")
	}
	// Both qa-tests invocations must use the derived supervisor timeout as
	// Run-RootGoTest's fourth argument. An inner-only budget would still let the
	// 15-minute default supervisor kill a healthy 30-minute Go run.
	if got := strings.Count(qaArm, "$envOverride $qaTestTimeout"); got != 2 {
		t.Fatalf("scripts/windows-task.ps1 qa-tests supervisor timeouts = %d, want 2", got)
	}
}

func TestQAFastPreflight_TestCost_QATaggedHotspotsPackageSetContract(t *testing.T) {
	t.Parallel()

	wantHotspots := []string{
		"./internal/archtest",
		"./internal/infra/runtimebundle",
		"./tools/backendplugin/...",
	}
	if !reflect.DeepEqual(testcost.QATaggedHotspotPackages(), wantHotspots) {
		t.Fatalf("testcost.QATaggedHotspotPackages() = %#v, want %#v", testcost.QATaggedHotspotPackages(), wantHotspots)
	}

	for _, hotspot := range wantHotspots {
		clean := strings.TrimPrefix(hotspot, "./")
		clean = strings.TrimSuffix(clean, "/...")
		dir := repositoryFile(t, strings.Split(clean, "/")...)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("hotspot root %s does not exist: %v", hotspot, err)
		}
		if len(entries) == 0 {
			t.Fatalf("hotspot root %s is empty", hotspot)
		}
	}
}
