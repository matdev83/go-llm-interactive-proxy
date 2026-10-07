package qa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Host Node-independence guard (spec cursor-sdk-standalone, task 5.1).
//
// The host build/verification surface must not depend on the Cursor toolchain.
// Two invariants are asserted here and neither is an absence claim:
//
//  1. The no-Node lane exists, is wired into CI and Make, removes Node from a
//     private mount namespace, and proves it with negative *and* positive
//     controls rather than with a keyword scan.
//  2. Every currently active Cursor source-path and Node setup/install
//     reference in the build, workflow and module inventories is recorded here
//     as a pending, retiring entry. Task 5.2/6.x delete those references and
//     shrink this table; until then the guard must not pretend they are gone.
//
// Historical specifications, operator documentation and the separately owned
// OpenResponses Node consumer are deliberately outside this inventory: they are
// not host build dependencies and a blanket keyword scan would forbid them.

const (
	nodeIndependenceTool           = "tools/backendplugin/node_independence/main.go"
	nodeIndependenceLinuxTool      = "tools/backendplugin/node_independence/isolate_linux.go"
	nodeIndependenceToolTests      = "tools/backendplugin/node_independence/main_test.go"
	nodeIndependenceLinuxTests     = "tools/backendplugin/node_independence/isolate_linux_test.go"
	adminGuardTest                 = "internal/stdhttp/admin_detection_linux_test.go"
	nodeIndependenceShell          = "check-node-independence.sh"
	nodeIndependencePowerShell     = "check-node-independence.ps1"
	nodeIndependenceWorkflow       = "node-independence.yml"
	nodeIndependenceWorkflowName   = "Node independence"
	nodeIndependenceJob            = "no-node"
	nodeIndependenceClassification = "node-independence"
)

// cursorSourcePathMarkers identify the in-tree Cursor SDK source path in
// build/CI inventories. `cursorcliacp` is a different product line and matches
// none of them, so it stays out of scope without a special case.
var cursorSourcePathMarkers = []string{"cursorsdk", "cursor-sdk", "cursor_sdk", "CURSOR_SDK", "@cursor/sdk"}

// nodeToolchainMarkers identify Node setup/install commands, i.e. the verbs
// that would make a host lane depend on the Cursor toolchain. Bare `node <file>`
// execution is an invocation, not a setup dependency, and is proven absent by
// the lane's own isolation plus negative controls.
var nodeToolchainMarkers = []string{"actions/setup-node", "node-version", "npm ci", "npm install", "npm test", "npm run "}

// pendingHostToolchainInventory records every active Cursor/Node reference that
// task 5.2 (host decouple) and tasks 6.1/6.2 (cutover) are scheduled to delete.
// Each entry is exact: a new marker or a new file fails immediately, and a
// removal fails until the entry is retired here in the same change.
var pendingHostToolchainInventory = map[string][]string{
	"Makefile": {
		"CURSOR_SDK", "cursor-sdk", "cursorsdk",
	},
	".golangci.yml": {
		"cursorsdk",
	},
	".github/dependabot.yml": {
		"cursorsdk",
	},
	".github/actions/go-cache/policy.json": {
		"cursorsdk",
	},
	".github/workflows/cursor-sdk-platform.yml": {
		"CURSOR_SDK", "actions/setup-node", "cursor-sdk", "cursorsdk", "node-version", "npm ci", "npm run ", "npm test",
	},
	// OpenResponses official compliance tooling is a separate product line with
	// its own pinned Node toolchain. It is not host build coupling and must not
	// be swept up by the Cursor cutover.
	".github/workflows/openresponses-official-compliance.yml": {
		"actions/setup-node", "node-version", "npm ci",
	},
	"scripts/check-adhoc-goroutines.ps1":            {"cursorsdk"},
	"scripts/check-adhoc-goroutines.sh":             {"cursorsdk"},
	"scripts/fuzz-targets.tsv":                      {"cursorsdk"},
	"scripts/makefile-scope.sh":                     {"cursor-sdk", "cursorsdk"},
	"scripts/prune-go-caches.py":                    {"cursorsdk"},
	"scripts/test-cursor-sdk-comparison-report.ps1": {"cursorsdk"},
	"scripts/test-cursor-sdk-comparison-report.sh":  {"cursorsdk"},
	"scripts/test-cursor-sdk-live-bridge.ps1":       {"CURSOR_SDK", "cursor-sdk", "cursorsdk"},
	"scripts/test-cursor-sdk-live-bridge.sh":        {"CURSOR_SDK", "cursor-sdk", "cursorsdk"},
	"scripts/test-cursor-sdk-live.ps1":              {"CURSOR_SDK", "cursor-sdk", "cursorsdk", "npm run "},
	"scripts/test-cursor-sdk-live.sh":               {"CURSOR_SDK", "cursor-sdk", "cursorsdk", "npm run "},
	"scripts/test-cursor-sdk-platform.ps1":          {"cursorsdk"},
	"scripts/test-cursor-sdk-platform.sh":           {"cursorsdk"},
	"scripts/test-openresponses-compliance.ps1":     {"npm ci"},
	"scripts/test-openresponses-compliance.sh":      {"npm ci"},
}

// nodeIndependenceGuardFiles are the guard's own artifacts. They necessarily
// name the tools they forbid, so they are excluded from the inventory scan by
// exact path rather than by weakening the detector.
var nodeIndependenceGuardFiles = map[string]bool{
	nodeIndependenceTool:                            true,
	nodeIndependenceLinuxTool:                       true,
	nodeIndependenceToolTests:                       true,
	nodeIndependenceLinuxTests:                      true,
	"scripts/" + nodeIndependenceShell:              true,
	"scripts/" + nodeIndependencePowerShell:         true,
	".github/workflows/" + nodeIndependenceWorkflow: true,
}

func TestQAFastPreflight_NodeIndependenceLaneContract(t *testing.T) {
	t.Parallel()

	tool := readRepositoryFile(t, strings.Split(nodeIndependenceTool, "/")...)
	for _, needle := range []string{
		// Real unavailability, proven rather than asserted, and re-probed after the
		// elevation so a toolchain that survived isolation fails the lane.
		"probeNodeToolchain", "after.Empty()", "ensureIsolated",
		// Non-administrative execution is asserted, not assumed.
		"requireNonRootEUID", "os.Geteuid()", "callerEnv",
		// Negative and positive controls are mandatory; a lane with only
		// negative controls would pass on a broken toolchain.
		"negativeControls", "positiveControls", "reportControlOutcome", "LaunchFailed",
		// The documented verification surface, including the CLI binary steps.
		"\"root-build\"", "\"unit\"", "\"quality\"", "\"comprehensive\"",
		"\"package-minimal\"", "\"cli-version\"", "\"cli-help\"", "\"non-cursor-smoke\"",
		"make", "test-unit", "quality-checks", "package-minimal", "parity-sentinel",
		"{lipstd}", "--root", "--set", "--isolate",
		// Defence in depth on top of isolation.
		"installTripwires", "LIP_NODE_TRIPWIRE_LOG",
		// Exit statuses are recorded rather than summarised away.
		"type stepResult", "Exit int", "--report",
		"OK node-independence",
	} {
		if !strings.Contains(tool, needle) {
			t.Errorf("%s missing no-Node lane contract %q", nodeIndependenceTool, needle)
		}
	}

	linuxTool := readRepositoryFile(t, strings.Split(nodeIndependenceLinuxTool, "/")...)
	for _, needle := range []string{
		// A mount namespace only: there is deliberately no user namespace, because
		// mapping the caller to root would leave the surface running as EUID 0.
		"--mount", "--fork", "--propagation", "private",
		"unix.Mount", "MS_BIND",
		// Minimal elevation, then the privileges go straight back down.
		"setpriv", "--reuid=", "--regid=", "--clear-groups", "syscall.Exec",
		// The drop identity may only ever come from sudo's own variables, and
		// the sudo caller must never be root.
		"SUDO_UID", "SUDO_GID", "sudoCallerIdentity", "sudoAvailable",
		// Everything handed across the privilege boundary is re-validated there:
		// sudo strips the environment, so both inputs travel as owner-only files.
		"stageHandoff", "readOwnerFile", "validatedMaskTargets", "isolationArgv",
		// Fail closed rather than degrade to a PATH-only proof.
		"isolationSupported", "isolationUnavailableReason",
	} {
		if !strings.Contains(linuxTool, needle) {
			t.Errorf("%s missing isolation contract %q", nodeIndependenceLinuxTool, needle)
		}
	}

	// The lane must be able to fail: an isolation path that always reports
	// success, or a fixture that is silently inert, would satisfy every other
	// assertion in this file. Each file therefore owns the negative cases only it
	// can express, including the privilege boundary and the admin guard the
	// non-administrative execution requirement rests on.
	for test, needles := range map[string][]string{
		nodeIndependenceToolTests: {
			"TestProbeDetectsEntryPointsByNameAndInsideTheRepository",
			"TestProbeIgnoresNonExecutableLookalikes",
			"TestProbeIgnoresTheLanesOwnTripwireDirectory",
			"TestNegativeControlFailsAWorkingCommandAndPassesAnAbsentOne",
			"TestReportControlOutcomeRejectsAnyUnsatisfiedControl",
			"TestParseArgsRequiresAnExplicitRoot",
			"t.Errorf",
		},
		nodeIndependenceLinuxTests: {
			"TestRootHelperMasksAndDropsPrivileges",
			"TestRootHelperRefusesAnUntrustedMaskList",
			"TestSudoCallerIdentityRefusesInventedIdentities",
			"TestRequireNonRootEUIDRejectsAdministrativeCaller",
			"TestMaskTargetsReportsUnmaskableTargets",
			"t.Errorf",
		},
		adminGuardTest: {
			"TestDetectRunningAsAdminMatchesTheProcessCredential",
			"detectRunningAsAdmin",
			"os.Geteuid()",
			"validateStartupSecurity",
			"t.Fatalf",
		},
	} {
		contents := readRepositoryFile(t, strings.Split(test, "/")...)
		for _, needle := range needles {
			if !strings.Contains(contents, needle) {
				t.Errorf("%s missing negative coverage %q", test, needle)
			}
		}
	}

	sh := readRepositoryFile(t, "scripts", nodeIndependenceShell)
	if !strings.Contains(sh, "go run ./tools/backendplugin/node_independence") {
		t.Errorf("scripts/%s must delegate to the Go lane tool", nodeIndependenceShell)
	}
	ps1 := readRepositoryFile(t, "scripts", nodeIndependencePowerShell)
	if !strings.Contains(ps1, "Linux-authoritative") {
		t.Errorf("scripts/%s must state that the lane is Linux-authoritative instead of degrading", nodeIndependencePowerShell)
	}

	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", nodeIndependenceWorkflow)), &workflow); err != nil {
		t.Fatalf("parse %s: %v", nodeIndependenceWorkflow, err)
	}
	if workflow.Name != nodeIndependenceWorkflowName {
		t.Errorf("%s name = %q, want %q", nodeIndependenceWorkflow, workflow.Name, nodeIndependenceWorkflowName)
	}
	job, ok := workflow.Jobs[nodeIndependenceJob]
	if !ok {
		t.Fatalf("%s is missing the authoritative %q job", nodeIndependenceWorkflow, nodeIndependenceJob)
	}
	if !strings.Contains(job.If, "always() && !cancelled()") {
		t.Errorf("%s/%s must report its own status independently of the scope classifier", nodeIndependenceWorkflow, nodeIndependenceJob)
	}
	var runs, uses []string
	for _, step := range job.Steps {
		if strings.TrimSpace(step.Run) != "" {
			runs = append(runs, step.Run)
		}
		uses = append(uses, step.Uses)
	}
	if !slices.ContainsFunc(runs, func(run string) bool {
		return strings.Contains(run, "scripts/"+nodeIndependenceShell) && strings.Contains(run, "--set")
	}) {
		t.Errorf("%s/%s must invoke scripts/%s with an explicit command set", nodeIndependenceWorkflow, nodeIndependenceJob, nodeIndependenceShell)
	}
	for _, use := range uses {
		if strings.HasPrefix(use, "actions/setup-node@") {
			t.Errorf("%s/%s must not provision Node in its own lane", nodeIndependenceWorkflow, nodeIndependenceJob)
		}
	}
	for _, marker := range nodeToolchainMarkers {
		if slices.ContainsFunc(runs, func(run string) bool { return strings.Contains(run, marker) }) {
			t.Errorf("%s/%s must not run %q", nodeIndependenceWorkflow, nodeIndependenceJob, marker)
		}
	}

	// The shared bounded Go cache policy owns identity and publication.
	var policy map[string]struct {
		Workflow string `json:"workflow"`
		Job      string `json:"job"`
		BuildMiB int    `json:"build_mib"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, ".github", "actions", "go-cache", "policy.json")), &policy); err != nil {
		t.Fatal(err)
	}
	lanes := make([]string, 0, len(policy))
	for lane, owner := range policy {
		if owner.Workflow != nodeIndependenceWorkflowName {
			continue
		}
		if owner.Job != nodeIndependenceJob {
			t.Errorf("cache lane %q is owned by job %q, want %q", lane, owner.Job, nodeIndependenceJob)
		}
		lanes = append(lanes, lane)
	}
	if len(lanes) != 1 {
		t.Errorf("%s needs exactly one registered Go cache lane, got %v", nodeIndependenceWorkflowName, lanes)
	}

	makefile := readRepositoryFile(t, "Makefile")
	if !strings.Contains(makefile, "node-independence:") {
		t.Error("Makefile is missing the node-independence target")
	}
	if !strings.Contains(makefile, "scripts/"+nodeIndependenceShell) || !strings.Contains(makefile, "scripts/"+nodeIndependencePowerShell) {
		t.Error("node-independence must route through both platform scripts")
	}
	if !strings.Contains(makefile, "make node-independence") {
		t.Error("Makefile help must document the node-independence lane")
	}
	// Every .PHONY target is classified in the archived design table, so the
	// classification must be maintained alongside the .PHONY entry.
	phony, _, _ := strings.Cut(makefile, "\n")
	if !strings.HasPrefix(phony, ".PHONY:") || !strings.Contains(phony, nodeIndependenceClassification) {
		t.Errorf(".PHONY must classify %q", nodeIndependenceClassification)
	}
	design := readRepositoryFile(t, ".kiro", "specs", "archive", "windows-task-reliability", "design.md")
	if !strings.Contains(design, "| `"+nodeIndependenceClassification+"` |") {
		t.Errorf("archived target table is missing the %q classification", nodeIndependenceClassification)
	}
}

func TestQAFastPreflight_NodeIndependenceActiveInventories(t *testing.T) {
	t.Parallel()

	observed := scanHostToolchainInventory(t, repoRoot(t))
	for path, expected := range pendingHostToolchainInventory {
		got, ok := observed[path]
		if !ok {
			t.Errorf("recorded pending Cursor/Node inventory %s no longer exists; retire the entry", path)
			continue
		}
		if !slices.Equal(got, expected) {
			t.Errorf("%s markers = %v, want %v; add or retire the entry deliberately", path, got, expected)
		}
	}
	for path, got := range observed {
		if _, ok := pendingHostToolchainInventory[path]; ok {
			continue
		}
		t.Errorf("unrecorded active Cursor/Node reference in %s: %v", path, got)
	}

	// cursorcliacp is a distinct external connector: the cutover must not
	// sweep it up, so its parity target has to remain reachable.
	if !strings.Contains(readRepositoryFile(t, "Makefile"), "parity-cursorcliacp-plugin:") {
		t.Error("cursorcliacp parity target must survive the Cursor SDK cutover")
	}
	for _, marker := range cursorSourcePathMarkers {
		if strings.Contains("cursorcliacp", marker) {
			t.Errorf("cursor source marker %q also matches the retained cursorcliacp connector", marker)
		}
	}
}

// scanHostToolchainInventory walks the active build, workflow and module
// inventories and reports, per file, the sorted Cursor/Node markers found.
// Documentation and specification trees are out of scope by construction.
func scanHostToolchainInventory(t *testing.T, root string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	record := func(relative string) {
		if nodeIndependenceGuardFiles[relative] {
			return
		}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := string(contents)
		var markers []string
		for _, marker := range slices.Concat(cursorSourcePathMarkers, nodeToolchainMarkers) {
			if strings.Contains(text, marker) {
				markers = append(markers, marker)
			}
		}
		if len(markers) == 0 {
			return
		}
		sort.Strings(markers)
		found[relative] = markers
	}
	for _, relative := range []string{
		"Makefile",
		".golangci.yml",
		".goreleaser.yaml",
		".release-files",
		"go.mod",
		"go.sum",
		".github/dependabot.yml",
		".github/actions/go-cache/policy.json",
	} {
		record(relative)
	}
	for _, directory := range []string{".github/workflows", "scripts"} {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(directory)))
		if err != nil {
			t.Fatalf("read %s: %v", directory, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			record(directory + "/" + entry.Name())
		}
	}
	return found
}
