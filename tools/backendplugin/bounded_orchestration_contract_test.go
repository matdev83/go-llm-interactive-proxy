package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backendpluginrunner "github.com/matdev83/go-llm-interactive-proxy/tools/backendplugin/runner"
	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

func TestBoundedOrchestration_ModulePhaseLabels(t *testing.T) {
	t.Parallel()
	result := backendpluginrunner.Run(context.Background(), backendpluginrunner.Request{
		Argv:    []string{os.Args[0], "-test.run=TestBoundedOrchestrationChildSuccess"},
		Dir:     t.TempDir(),
		Env:     []string{"GO_WANT_HELPER_PROCESS=1"},
		Timeout: time.Second,
		Output:  taskrunner.Capture,
		Label:   "module:example:build",
	})
	if result.Kind != taskrunner.Success || result.Label != "module:example:build" {
		t.Fatalf("unexpected result: kind=%s label=%q err=%v", result.Kind, result.Label, result.Err)
	}
}

func TestBoundedOrchestration_StopsDependentPhase(t *testing.T) {
	t.Parallel()
	result := backendpluginrunner.Run(context.Background(), backendpluginrunner.Request{
		Argv:    []string{os.Args[0], "-test.run=TestBoundedOrchestrationChildFailure"},
		Dir:     t.TempDir(),
		Env:     []string{"GO_WANT_HELPER_PROCESS=1"},
		Timeout: time.Second,
		Output:  taskrunner.Capture,
		Label:   "module:example:test",
	})
	if result.Kind != taskrunner.ChildFailure {
		t.Fatalf("expected child failure propagation, got %s (%v)", result.Kind, result.Err)
	}
	if err := backendpluginrunner.Error(result); !strings.Contains(err.Error(), "module:example:test") {
		t.Fatalf("missing phase label in error: %v", err)
	}
}

func TestBoundedOrchestration_CleansDescendants(t *testing.T) {
	t.Parallel()
	result := backendpluginrunner.Run(context.Background(), backendpluginrunner.Request{
		Argv:    []string{os.Args[0], "-test.run=TestBoundedOrchestrationChildSleep"},
		Dir:     t.TempDir(),
		Env:     []string{"GO_WANT_HELPER_PROCESS=1"},
		Timeout: 25 * time.Millisecond,
		Output:  taskrunner.Capture,
		Label:   "module:example:timeout",
	})
	if result.Kind != taskrunner.DeadlineExceeded {
		t.Fatalf("expected timeout propagation, got %s (%v)", result.Kind, result.Err)
	}
	if !result.Cleanup.Attempted {
		t.Fatal("timeout did not attempt process-tree cleanup")
	}
}

func TestBoundedOrchestrationChildSuccess(t *testing.T) { t.Parallel() }

func TestBoundedOrchestrationChildFailure(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Exit(23)
	}
}

func TestBoundedOrchestrationChildSleep(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		time.Sleep(time.Hour)
	}
}

func readRepoRootFile(t *testing.T, name ...string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(".", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(append([]string{root}, name...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(name...), err)
	}
	return string(body)
}

func TestBoundedOrchestration_StaticAndFullProfiles(t *testing.T) {
	t.Parallel()
	mainGo := readRepoRootFile(t, "tools", "backendplugin", "release_gates", "main.go")
	makefile := readRepoRootFile(t, "Makefile")
	windowsTask := readRepoRootFile(t, "scripts", "windows-task.ps1")

	if !strings.Contains(mainGo, `"mode", "static"`) {
		t.Fatal("static release mode must remain the default QA path")
	}
	if !strings.Contains(mainGo, `"full"`) {
		t.Fatal("full release mode must remain an explicit opt-in")
	}
	for _, marker := range []string{
		"-mode=static", "-Timeout \"15m\"", "backend-plugin-release-gates-static", "release_gates",
		"-mode=full", "-Timeout \"120m\"",
	} {
		if !strings.Contains(windowsTask, marker) {
			t.Fatalf("windows task router lost %q", marker)
		}
	}
	for _, file := range []string{
		"release_gates/main.go", "release_gates/catalog.go",
		"release_gates/conformance.go", "release_gates/tidy_check.go",
	} {
		body := readRepoRootFile(t, "tools", "backendplugin", file)
		if !strings.Contains(body, "Label:") || !strings.Contains(body, "release_gates:") {
			t.Errorf("%s must label every runner request with a release_gates: prefix", file)
		}
	}
	if !strings.Contains(makefile, "backend-plugin-release-gates: backend-plugin-release-gates-static") {
		t.Fatal("full release mode must build on the static release gate")
	}
	if !strings.Contains(makefile, "qa: quality-checks qa-tests lint vuln backend-plugin-release-gates-static") {
		t.Fatal("make qa must keep static-only release wiring (full profile stays opt-in)")
	}
	if strings.Contains(makefile, "backend-plugin-release-gates-static: backend-plugin-release-gates") {
		t.Fatal("static release must not depend on the full release profile")
	}
}

func TestBoundedOrchestration_NoUnboundedProductionExec(t *testing.T) {
	t.Parallel()
	root := filepath.Join(".")
	files := []string{
		"crossplatform_qa/main.go",
		"package_plugins/main.go",
		"isolated_root_qa/main.go",
		"installed_plugin_smoke/main.go",
		"release_gates/main.go",
		"release_gates/catalog.go",
		"release_gates/conformance.go",
		"release_gates/tidy_check.go",
	}
	for _, name := range files {
		path := filepath.Join(root, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if strings.Contains(text, "exec.Command(") || strings.Contains(text, ".CombinedOutput()") || strings.Contains(text, ".Output()") {
			t.Errorf("%s still contains an unbounded production subprocess", name)
		}
	}
}
