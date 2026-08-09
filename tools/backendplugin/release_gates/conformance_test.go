package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/tools/backendplugin/runner"
	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

//nolint:paralleltest // mutates the package-level runCommand seam.
func TestGoTestListHasMatches_FailsClosedOnFailedLookingOutput(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	runCommand = func(context.Context, runner.Request) taskrunner.Result {
		return taskrunner.Result{
			Kind:   taskrunner.ChildFailure,
			Stdout: []byte("TestConformance_Fake\nok\tfake\n"),
			Stderr: []byte("unique-selector-failure"),
			Err:    errors.New("exit status 9"),
		}
	}

	count, err := goTestListHasMatches(t.TempDir(), "./fake", "TestConformance_")
	if err == nil {
		t.Fatal("failed selector unexpectedly succeeded")
	}
	if count != 0 {
		t.Fatalf("failed selector count = %d, want 0", count)
	}
	if got := strings.Count(err.Error(), "unique-selector-failure"); got != 1 {
		t.Fatalf("failure marker count = %d in %q", got, err)
	}
}

//nolint:paralleltest // mutates the package-level runCommand seam.
func TestListMatchingTests_FailsClosedOnFailedLookingOutput(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	runCommand = func(context.Context, runner.Request) taskrunner.Result {
		return taskrunner.Result{
			Kind:   taskrunner.ChildFailure,
			Stdout: []byte(`{"Action":"output","Output":"TestConformance_Fake\\n"}` + "\n"),
			Err:    errors.New("exit status 9"),
		}
	}

	names, err := listMatchingTests(t.TempDir(), conformanceNameRe)
	if err == nil {
		t.Fatal("failed discovery unexpectedly succeeded")
	}
	if names != nil {
		t.Fatalf("failed discovery returned names: %v", names)
	}
}

//nolint:paralleltest // mutates the package-level runCommand seam.
func TestRunConformanceFilter_DoesNotDuplicateFailureOutput(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	calls := 0
	runCommand = func(context.Context, runner.Request) taskrunner.Result {
		calls++
		if calls == 1 {
			return taskrunner.Result{
				Kind:   taskrunner.Success,
				Stdout: []byte(`{"Action":"run","Test":"TestConformance_Fake"}` + "\n"),
			}
		}
		return taskrunner.Result{
			Kind:   taskrunner.ChildFailure,
			Stdout: []byte("unique-conformance-failure"),
			Err:    errors.New("exit status 7"),
		}
	}

	_, _, err := runConformanceFilter(t.TempDir())
	if err == nil {
		t.Fatal("failed conformance run unexpectedly succeeded")
	}
	if got := strings.Count(err.Error(), "unique-conformance-failure"); got != 1 {
		t.Fatalf("failure marker count = %d in %q", got, err)
	}
}

//nolint:paralleltest // reads module source tree; keep serial for stable go list
func TestListMatchingTests_LocalstubConformance(t *testing.T) {
	root := repoRoot(t)
	mod := filepath.Join(root, "connectors", "localstub")
	names, err := listMatchingTests(mod, conformanceNameRe)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names {
		if n == "TestConformance_ServiceSuite" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected TestConformance_ServiceSuite, got %v", names)
	}
}

//nolint:paralleltest // reads module source tree; keep serial for stable go list
func TestListMatchingTests_CodexHasParity(t *testing.T) {
	root := repoRoot(t)
	mod := filepath.Join(root, "connectors", "codex")
	names, err := listMatchingTests(mod, conformanceNameRe)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("codex must discover advertised-capability tests")
	}
}

//nolint:paralleltest // reads module source tree; keep serial for stable go list
func TestValidateSelectors_Root(t *testing.T) {
	if err := validateSelectors(repoRoot(t)); err != nil {
		t.Fatal(err)
	}
}
