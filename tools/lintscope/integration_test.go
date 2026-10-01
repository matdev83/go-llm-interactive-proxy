package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func scopeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                        "module example.com/scope\n\ngo 1.26.0\n",
		"base/base.go":                  "package base\nconst Value = 1\n",
		"consumer/consumer.go":          "package consumer\nimport \"example.com/scope/base\"\nconst Value = base.Value\n",
		"testconsumer/consumer.go":      "package testconsumer\n",
		"testconsumer/consumer_test.go": "package testconsumer_test\nimport (\"testing\"; \"example.com/scope/consumer\")\nfunc TestValue(t *testing.T) { if consumer.Value != 1 { t.Fatal(consumer.Value) } }\n",
		"unrelated/unrelated.go":        "package unrelated\n",
	}
	for name, body := range files {
		writeScopeFile(t, root, name, body)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Scope Test", "-c", "user.email=scope@example.invalid", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}

func writeScopeFile(t *testing.T, root, name, body string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalLintPlanReadsActualGitAndTestImportGraph(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "base/base.go", "package base\nconst Value = 2\n")
	plan, err := buildLintPlan(context.Background(), root, "changed")
	if err != nil {
		t.Fatal(err)
	}
	want := []moduleScope{{Directory: ".", Packages: []string{"./base", "./consumer", "./testconsumer"}}}
	if plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("plan=%+v want %v", plan, want)
	}
}

func TestLocalLintPlanUntrackedNestedModuleUsesModuleRelativeScope(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "connectors/test/go.mod", "module example.com/connector\n\ngo 1.26.0\n")
	writeScopeFile(t, root, "connectors/test/backend/backend.go", "package backend\n")
	// Dependency changes deliberately force the comprehensive fallback.
	plan, err := buildLintPlan(context.Background(), root, "changed")
	if err != nil || !plan.Full {
		t.Fatalf("untracked dependency change plan=%+v err=%v", plan, err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	cmd = exec.Command("git", "-c", "user.name=Scope Test", "-c", "user.email=scope@example.invalid", "commit", "-qm", "connector")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	writeScopeFile(t, root, "connectors/test/backend/backend.go", "package backend\nconst Value = 2\n")
	plan, err = buildLintPlan(context.Background(), root, "changed")
	if err != nil {
		t.Fatal(err)
	}
	want := []moduleScope{{Directory: "connectors/test", Packages: []string{"./backend"}}}
	if plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("nested plan=%+v want %v", plan, want)
	}
}

func TestLocalLintPlanDoesNotHideDiscoveryFailure(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "base/base.go", "package base\nimport _ \"example.invalid/unavailable\"\n")
	if _, err := buildLintPlan(context.Background(), root, "changed"); err == nil {
		t.Fatal("failed graph discovery must fail closed")
	}
}

func TestLocalLintPlanDocumentationAndEmptyIndex(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "docs/note.md", "documentation\n")
	for _, mode := range []string{"changed", "staged"} {
		plan, err := buildLintPlan(context.Background(), root, mode)
		if err != nil || plan.Full || len(plan.Modules) != 0 {
			t.Fatalf("%s plan=%+v err=%v", mode, plan, err)
		}
	}
}

func TestLocalLintPlanCleanCheckoutRetainsOnlyTheRootGate(t *testing.T) {
	plan, err := buildLintPlan(context.Background(), scopeFixture(t), "changed")
	want := []moduleScope{{Directory: ".", Packages: []string{"./..."}}}
	if err != nil || plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("clean plan=%+v err=%v", plan, err)
	}
}

func TestLocalLintPlanStagedScopeDoesNotIncludeUnstagedPackages(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "base/base.go", "package base\nconst Value = 2\n")
	cmd := exec.Command("git", "add", "base/base.go")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	writeScopeFile(t, root, "unrelated/new.go", "package unrelated\nconst New = 1\n")
	plan, err := buildLintPlan(context.Background(), root, "staged")
	want := []moduleScope{{Directory: ".", Packages: []string{"./base", "./consumer", "./testconsumer"}}}
	if err != nil || plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("staged plan=%+v err=%v want %v", plan, err, want)
	}
}

func TestLocalLintPlanDoesNotHideGitFailure(t *testing.T) {
	if _, err := buildLintPlan(context.Background(), t.TempDir(), "changed"); err == nil {
		t.Fatal("failed Git discovery must fail closed")
	}
}
