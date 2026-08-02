package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryFile(t *testing.T, name ...string) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{root, "..", ".."}, name...)...)
}

func readRepositoryFile(t *testing.T, name ...string) string {
	t.Helper()
	contents, err := os.ReadFile(repositoryFile(t, name...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(name...), err)
	}
	return string(contents)
}

func workflowJob(t *testing.T, name, job string) string {
	t.Helper()
	contents := readRepositoryFile(t, ".github", "workflows", name)
	if !strings.Contains(contents, "\n  "+job+":") {
		t.Errorf("%s is missing the authoritative job %q", name, job)
	}
	return contents
}

func makeTargetBlock(makefile, target string) string {
	marker := target + ":"
	start := strings.Index(makefile, marker)
	if start < 0 {
		return ""
	}
	rest := makefile[start:]
	lines := strings.Split(rest, "\n")
	var block []string
	for i, line := range lines {
		conditional := strings.HasPrefix(strings.TrimSpace(line), "ifeq ") || strings.HasPrefix(strings.TrimSpace(line), "ifneq ") || strings.TrimSpace(line) == "else" || strings.TrimSpace(line) == "endif"
		if i > 0 && line != "" && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") && !conditional {
			break
		}
		block = append(block, line)
	}
	return strings.Join(block, "\n")
}

func hasWindowsTaskRecipe(makefile, target string) bool {
	for line := range strings.SplitSeq(makefile, "\n") {
		if strings.TrimSpace(line) == "@$(WINDOWS_TASK) "+target {
			return true
		}
	}
	return false
}

func TestWindowsTaskReliability_TargetTableComplete(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	design := readRepositoryFile(t, ".kiro", "specs", "archive", "windows-task-reliability", "design.md")
	phonyLine := strings.SplitN(makefile, "\n", 2)[0]
	if !strings.HasPrefix(phonyLine, ".PHONY:") {
		t.Fatal("Makefile must keep its .PHONY classification table on the first line")
	}
	for target := range strings.FieldsSeq(strings.TrimPrefix(phonyLine, ".PHONY:")) {
		if !strings.Contains(design, "| `"+target+"` |") {
			t.Errorf(".PHONY target %q is missing from the approved classification table", target)
		}
	}
}

func TestWindowsTaskReliability_WindowsRoutes(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	targets := []string{
		"quality-checks", "test-unit", "test-fuzz", "lint", "parity-checks",
		"parity-acp-plugin", "parity-cursorcliacp-plugin", "parity-cli-acp-plugins",
		"parity-openrouter-plugin", "parity-hosted-compatible-plugins", "parity-ollama-plugins",
		"parity-opencode-plugins", "parity-codex-plugins", "parity-local-compatible-plugins",
		"test-local-compatible-plugin-modules", "backend-plugin-module-checks",
		"backend-plugin-security-checks", "backend-plugin-cross-platform-qa",
		"backend-plugin-release-gates-static", "qa",
	}
	for _, target := range targets {
		hasScript := (target == "quality-checks" && strings.Contains(makefile, "scripts/quality-checks.ps1")) || (target == "backend-plugin-module-checks" && strings.Contains(makefile, "scripts/backend-plugin-module-checks.ps1"))
		if !hasWindowsTaskRecipe(makefile, target) && target != "qa" && !hasScript {
			t.Errorf("target %q has no explicit Windows runner/script route", target)
		}
	}
	if !strings.Contains(makefile, "qa: quality-checks qa-tests lint vuln backend-plugin-release-gates-static") {
		t.Fatal("qa must retain its approved Windows-routed prerequisite classification")
	}

	for _, script := range []string{"scripts/quality-checks.ps1", "scripts/backend-plugin-module-checks.ps1"} {
		contents := readRepositoryFile(t, strings.Split(script, "/")...)
		if !strings.Contains(contents, "Invoke-TaskRunner") {
			t.Errorf("%s does not route child commands through lip-taskrunner", script)
		}
		if strings.Contains(contents, "$env:GOWORK =") {
			t.Errorf("%s mutates the parent GOWORK environment", script)
		}
	}
}

func TestWindowsTaskReliability_LocalCompatibleExecutionRoutes(t *testing.T) {
	t.Parallel()
	script := readRepositoryFile(t, "scripts", "windows-task.ps1")
	local := strings.Index(script, `"test-local-compatible-plugin-modules" {`)
	parity := strings.Index(script, `"parity-local-compatible-plugins" {`)
	if local < 0 || parity < 0 || local >= parity {
		t.Fatal("local-compatible cases must be explicit and ordered")
	}
	localCase := script[local:parity]
	parityEnd := strings.Index(script[parity:], "\n        }")
	if parityEnd < 0 {
		t.Fatal("parity-local-compatible-plugins case is incomplete")
	}
	parityCase := script[parity : parity+parityEnd]
	if !strings.Contains(localCase, "Run-LocalCompatibleModules") || strings.Contains(localCase, "Run-RootGoTest") {
		t.Fatal("module-only target must run exactly the three local-compatible module tests")
	}
	if !strings.Contains(parityCase, "Run-LocalCompatibleModules") || !strings.Contains(parityCase, "internal/archtest") {
		t.Fatal("parity target must run modules and Phase7 archtest")
	}
}

func TestWindowsTaskReliability_LocalCompatibleModulesRunOnce(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	if !hasWindowsTaskRecipe(makefile, "parity-local-compatible-plugins") {
		t.Fatal("parity-local-compatible-plugins must retain a Windows task route")
	}
	lines := strings.Split(makefile, "\n")
	var rules []int
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "parity-local-compatible-plugins:") {
			rules = append(rules, i)
		}
	}
	if len(rules) != 2 {
		t.Fatalf("expected a guarded prerequisite rule plus the recipe rule for parity-local-compatible-plugins, found %d rule lines", len(rules))
	}
	if !strings.Contains(lines[rules[0]], "test-local-compatible-plugin-modules") {
		t.Fatal("POSIX prerequisite for the three local-compatible module tests is missing")
	}
	guarded := false
	for j := rules[0] - 1; j >= 0 && j >= rules[0]-2; j-- {
		if strings.TrimSpace(lines[j]) == "ifneq ($(OS),Windows_NT)" {
			guarded = true
			break
		}
	}
	if !guarded {
		t.Fatal("the test-local-compatible-plugin-modules prerequisite must be guarded by ifneq ($(OS),Windows_NT) so the Windows task route does not run the three module tests twice")
	}
	recipeEnd := len(lines)
	for j := rules[1] + 1; j < len(lines); j++ {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed != "" && !strings.HasPrefix(lines[j], "\t") && !strings.HasPrefix(lines[j], " ") && !strings.HasPrefix(trimmed, "if") && trimmed != "else" && trimmed != "endif" {
			recipeEnd = j
			break
		}
	}
	recipe := strings.Join(lines[rules[1]:recipeEnd], "\n")
	if !strings.Contains(recipe, "@$(WINDOWS_TASK) parity-local-compatible-plugins") {
		t.Fatal("Windows branch of parity-local-compatible-plugins must route through windows-task.ps1, which runs modules + Phase7 exactly once")
	}
	if !strings.Contains(recipe, "./internal/archtest -run 'Phase7_'") {
		t.Fatal("POSIX branch of parity-local-compatible-plugins must run only the Phase 7 archtest")
	}
	for _, module := range []string{"connectors/llamacpp", "connectors/lmstudio", "connectors/vllm"} {
		if strings.Contains(recipe, module) {
			t.Fatalf("POSIX recipe of parity-local-compatible-plugins must not re-run %s; the guarded prerequisite owns the module tests", module)
		}
	}
}

func TestWindowsTaskReliability_DedicatedBackendSelectorsAreTaggedAndNonVacuous(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	for _, target := range []string{"package-plugin-smoke", "backend-plugin-cross-platform-qa", "backend-plugin-release-gates-static"} {
		block := makeTargetBlock(makefile, target)
		if !strings.Contains(block, "-tags=integration") {
			t.Errorf("%s must compile tagged backend-plugin tests", target)
		}
		if !strings.Contains(block, "matched zero tests") || !strings.Contains(block, "-list") {
			t.Errorf("%s must reject a zero-match selector before execution", target)
		}
	}
	windows := readRepositoryFile(t, "scripts", "windows-task.ps1")
	for _, selector := range []string{"TestCrossPlatformQA_", "TestPackage_", "TestDiscoverModules_", "TestReleaseGates_"} {
		if !strings.Contains(windows, "-tags=integration") || !strings.Contains(windows, selector) {
			t.Errorf("Windows dedicated selector %q must use integration tags", selector)
		}
	}
}

func TestWindowsTaskReliability_FuzzAndParitySelectors(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	for _, selector := range []string{
		"FuzzJSONRoundTrip", "FuzzParseNDJSONLine", "FuzzMapSessionUpdateToEvents",
		"KillProcessTree_", "ProcessTree_CrossCompile", "TestParity_", "GOWORK=off",
	} {
		if !strings.Contains(makefile, selector) {
			t.Errorf("approved selector %q is missing", selector)
		}
	}
	for _, forbidden := range []string{"cd ", "GOWORK=off ", "&&", "/dev/null"} {
		if strings.Contains(readRepositoryFile(t, "scripts", "windows-task.ps1"), forbidden) {
			t.Errorf("Windows task router retains POSIX syntax %q", forbidden)
		}
	}
}

func TestWindowsTaskReliability_RaceSkip(t *testing.T) {
	t.Parallel()
	contents := readRepositoryFile(t, "scripts", "race-check.ps1")
	if !strings.Contains(contents, "Write-Host \"SKIP:") {
		t.Fatal("Windows race route must emit an explicit SKIP diagnostic")
	}
	if !strings.Contains(contents, "exit 0") || strings.Contains(contents, "if ($Strict)") {
		t.Fatal("Windows race route must be an unconditional successful unsupported-platform skip")
	}
	if !strings.Contains(readRepositoryFile(t, "scripts", "race-check.sh"), "go test -race") {
		t.Fatal("POSIX race route must retain authoritative race execution")
	}
}

func TestWindowsTaskReliability_PostgresAndExternalBlockers(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	for _, target := range []string{"test-postgres-migrations", "test-authority-postgres-direct", "test-authority-postgres-pooled", "lint", "vuln"} {
		block := makeTargetBlock(makefile, target)
		if block == "" {
			t.Errorf("missing prerequisite-sensitive target %q", target)
		}
	}
	if !strings.Contains(makefile, "Install golangci-lint") {
		t.Fatal("missing lint installation guidance")
	}
	if !strings.Contains(makefile, "govulncheck") {
		t.Fatal("missing vulnerability tool invocation")
	}
	catalog := readRepositoryFile(t, "tools", "backendplugin", "release_gates", "catalog.go")
	if !strings.Contains(catalog, "windows skip recorded as external_blocker; linux/macOS execute") {
		t.Fatal("release-gates race_scan must record a Windows skip as external_blocker, never as Linux race evidence")
	}
}

func TestWindowsTaskReliability_LinuxEvidence(t *testing.T) {
	t.Parallel()

	release := workflowJob(t, "release.yml", "verify")
	if !strings.Contains(release, "go test -race ./...") {
		t.Fatal("release verify job no longer carries root race evidence")
	}

	qa := workflowJob(t, "qa.yml", "qa")
	for _, command := range []string{
		"go test -timeout=8m ./internal/archtest",
		"go vet ./cmd/lipstd",
		"go test -timeout=5m ./cmd/lipstd",
	} {
		if !strings.Contains(qa, command) {
			t.Errorf("qa job lost PR QA command %q", command)
		}
	}

	nightly := workflowJob(t, "race-fuzz-nightly.yml", "race-fuzz")
	for _, command := range []string{
		"go test -race -timeout=20m",
		"make backend-plugin-security-checks",
		"make test-fuzz",
	} {
		if !strings.Contains(nightly, command) {
			t.Errorf("race-fuzz job lost %q", command)
		}
	}
	if !strings.Contains(nightly, "schedule:") {
		t.Fatal("race-fuzz nightly must retain its scheduled status")
	}

	gates := workflowJob(t, "backend-plugin-release-gates.yml", "release-gates")
	if !strings.Contains(gates, "make backend-plugin-release-gates") || !strings.Contains(gates, "timeout-minutes: 120") {
		t.Fatal("release-gates job must run the full backend-plugin gate under its 120-minute job limit")
	}

	matrix := workflowJob(t, "backend-plugin-cross-platform.yml", "cross-platform-qa")
	if !strings.Contains(matrix, "make backend-plugin-cross-platform-qa") {
		t.Fatal("cross-platform-qa job no longer runs the native platform matrix command")
	}

	security := workflowJob(t, "security.yml", "govulncheck")
	if !strings.Contains(security, "govulncheck ./...") {
		t.Fatal("govulncheck job no longer scans dependencies and reachable code")
	}

	design := readRepositoryFile(t, ".kiro", "specs", "archive", "windows-task-reliability", "design.md")
	for _, classification := range []string{
		"| `test-race` | explicit unsupported SKIP |",
		"| `release-gates` | Linux-authoritative |",
		"| `backend-plugin-security-checks` | Linux-authoritative |",
	} {
		if !strings.Contains(design, classification) {
			t.Errorf("design classification %q must stay Linux-authoritative/unsupported so Windows results cannot become evidence", classification)
		}
	}

	requirements := readRepositoryFile(t, ".kiro", "specs", "archive", "windows-task-reliability", "requirements.md")
	if !strings.Contains(requirements, "Windows `SKIP` or `external_blocker` results can never satisfy") {
		t.Fatal("requirements must retain the contract that Windows SKIP/external_blocker can never satisfy Linux race, security, conformance, fuzz, or release proofs")
	}

	for _, workflow := range []string{"release.yml", "race-fuzz-nightly.yml"} {
		contents := readRepositoryFile(t, ".github", "workflows", workflow)
		if strings.Contains(contents, "race-check.ps1") || strings.Contains(contents, "SKIP:") {
			t.Errorf("%s must not route Linux race evidence through the Windows SKIP branch", workflow)
		}
	}
}

func TestWindowsTaskReliability_CallSiteAudit(t *testing.T) {
	t.Parallel()
	makefile := readRepositoryFile(t, "Makefile")
	for _, line := range strings.Split(makefile, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "-fuzz=") && !strings.Contains(trimmed, "$(FUZZ_WRAPPER)") {
			t.Errorf("unbounded fuzz invocation bypasses the FUZZ_WRAPPER: %q", trimmed)
		}
	}
	if !strings.Contains(makefile, "@$(WINDOWS_TASK) test-fuzz") {
		t.Fatal("Windows test-fuzz branch must route through the bounded task runner")
	}
	for _, script := range []string{"scripts/quality-checks.ps1", "scripts/backend-plugin-module-checks.ps1", "scripts/windows-task.ps1"} {
		contents := readRepositoryFile(t, strings.Split(script, "/")...)
		if !strings.Contains(contents, "Invoke-TaskRunner") {
			t.Errorf("%s does not route child commands through lip-taskrunner", script)
		}
		if strings.Contains(contents, "$env:GOWORK =") || strings.Contains(contents, "$env:FUZZTIME =") {
			t.Errorf("%s mutates a parent environment variable", script)
		}
	}
	if !strings.Contains(readRepositoryFile(t, "scripts", "windows-task.ps1"), "-fuzztime=$fuzzTime") {
		t.Fatal("windows fuzz route no longer uses a local FUZZTIME value")
	}

	prod := []string{
		"release_gates/main.go",
		"release_gates/catalog.go",
		"release_gates/conformance.go",
		"release_gates/tidy_check.go",
		"package_plugins/main.go",
		"crossplatform_qa/main.go",
		"isolated_root_qa/main.go",
		"installed_plugin_smoke/main.go",
	}
	for _, name := range prod {
		contents := readRepositoryFile(t, "tools", "backendplugin", name)
		if !strings.Contains(contents, "runner.Request") {
			t.Errorf("tools/backendplugin/%s does not route children through the bounded runner seam", name)
		}
		for _, leak := range []string{"exec.Command(", ".CombinedOutput()", ".Output()"} {
			if strings.Contains(contents, leak) {
				t.Errorf("tools/backendplugin/%s still contains an unbounded subprocess call: %s", name, leak)
			}
		}
	}
	if !strings.Contains(readRepositoryFile(t, "tools", "backendplugin", "runner", "runner.go"), "taskrunner.Run(") {
		t.Fatal("backendplugin runner wrapper must delegate to tools/taskrunner")
	}
	archReport := readRepositoryFile(t, "scripts", "arch-report.go")
	if !strings.Contains(archReport, "exec.CommandContext(") {
		t.Fatal("scripts/arch-report.go must retain the documented parent-bounded CommandContext exemption")
	}
	if !strings.Contains(readRepositoryFile(t, "tools", "backendplugin", "installed_plugin_smoke", "main.go"), "runner.Run(ctx") {
		t.Fatal("installed-plugin-smoke serve must keep the parent-bounded context-owned invocation")
	}
}

func TestWindowsTaskReliability_TaskRunnerCaptureContract(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell taskrunner contract is Windows-specific")
	}
	t.Parallel()
	root := repositoryFile(t)
	command := "$items = @(Invoke-TaskRunner -Label 'contract' -Cwd '" + strings.ReplaceAll(root, "'", "''") + "' -Timeout '1s' -Output capture -Command @('powershell', '-NoProfile', '-Command', 'Write-Output module-a; Write-Output module-b; Write-Output module-c')); Write-Output ('count=' + $items.Count); $items | ForEach-Object { Write-Output $_ }"
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ". '"+strings.ReplaceAll(filepath.Join(root, "scripts", "taskrunner.ps1"), "'", "''")+"'; "+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capture contract: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\r\n")
	if len(lines) != 4 || lines[0] != "count=3" || strings.Join(lines[1:], "\n") != "module-a\nmodule-b\nmodule-c" {
		t.Fatalf("capture returned duplicated or unexpected discovery output: %q", output)
	}
}

func TestWindowsTaskReliability_RobocopyExitCodeClassifier(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("robocopy exit-code protocol is Windows-specific")
	}
	t.Parallel()
	script := repositoryFile(t, "scripts", "backend-plugin-module-checks.ps1")
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-SelfTest")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robocopy classifier self-test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "robocopy exit-code classifier self-test") {
		t.Fatalf("robocopy classifier self-test marker missing: %q", output)
	}
}

func TestWindowsTaskReliability_TaskRunnerCaptureFailureDiagnosticOnce(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell taskrunner contract is Windows-specific")
	}
	t.Parallel()
	root := repositoryFile(t)
	marker := "unique-powershell-taskrunner-failure-marker"
	command := "$ErrorActionPreference = 'Continue'; try { Invoke-TaskRunner -Label 'contract-failure' -Cwd '" + strings.ReplaceAll(root, "'", "''") + "' -Timeout '1s' -Output capture -Command @('powershell', '-NoProfile', '-Command', 'Write-Output " + marker + "; exit 23') } catch { Write-Output ('caught=' + $_.Exception.Message) }"
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ". '"+strings.ReplaceAll(filepath.Join(root, "scripts", "taskrunner.ps1"), "'", "''")+"'; "+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failure contract: %v\n%s", err, output)
	}
	if got := strings.Count(string(output), marker); got != 1 {
		t.Fatalf("failure marker count = %d, want 1: %q", got, output)
	}
}
