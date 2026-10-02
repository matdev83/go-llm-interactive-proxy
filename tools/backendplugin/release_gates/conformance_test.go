package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
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

//nolint:paralleltest // mutates the package-level runCommand seam.
func TestValidateSelectors_BatchesPackagesAndKeepsPackageIdentity(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	calls := 0
	runCommand = func(_ context.Context, req runner.Request) taskrunner.Result {
		calls++
		var patterns []string
		for _, check := range selectorChecks() {
			patterns = append(patterns, "("+check.pattern+")")
		}
		want := []string{"go", "test", "-json", "-list", strings.Join(patterns, "|")}
		for _, check := range selectorChecks() {
			want = append(want, check.pkg)
		}
		if !slices.Equal(req.Argv, want) {
			t.Fatalf("argv = %v, want %v", req.Argv, want)
		}
		// A name matching every selector in an unrelated package must not
		// satisfy any of the package-specific checks.
		return taskrunner.Result{Kind: taskrunner.Success, Stdout: []byte(`{"Action":"output","Package":"unrelated","Output":"TestParseStrict_TestHundredTestPostOutput_TestLeak_TestDigestHandleTestMixed_\n"}` + "\n")}
	}
	if err := validateSelectors(t.TempDir()); err == nil {
		t.Fatal("unrelated package unexpectedly satisfied selectors")
	}
	if calls != 1 {
		t.Fatalf("command calls = %d, want 1", calls)
	}
}

//nolint:paralleltest // mutates the package-level runCommand seam.
func TestValidateSelectors_FailsClosedOnChildFailure(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	runCommand = func(context.Context, runner.Request) taskrunner.Result {
		return taskrunner.Result{Kind: taskrunner.ChildFailure, Stdout: []byte("TestHundred\n"), Err: errors.New("unique-batch-failure")}
	}
	if err := validateSelectors(t.TempDir()); err == nil || strings.Count(err.Error(), "unique-batch-failure") != 1 {
		t.Fatalf("expected child failure exactly once, got %v", err)
	}
}

//nolint:paralleltest // mutates the package-level runCommand seam.
func TestValidateSelectors_RequiresEveryPackageAndCompleteOutput(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	checks := selectorChecks()
	names := []string{"TestParseStrict_X", "TestHundredX", "TestRecv_StressX", "TestPeer_X", "TestDigestHandleX", "TestMixed_X"}
	var output strings.Builder
	for i, check := range checks {
		event := listEvent{Action: "output", Package: "example.org/lip/" + strings.Trim(check.pkg, "./"), Output: names[i] + "\n"}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
		output.WriteByte('\n')
	}
	for _, tc := range []struct {
		name      string
		output    string
		truncated bool
		wantError bool
	}{
		{"complete", output.String(), false, false},
		{"missing_package", strings.SplitN(output.String(), "\n", 2)[1], false, true},
		{"malformed", output.String() + "not-json\n", false, true},
		{"truncated", output.String(), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCommand = func(context.Context, runner.Request) taskrunner.Result {
				return taskrunner.Result{Kind: taskrunner.Success, Stdout: []byte(tc.output), StdoutTruncated: tc.truncated}
			}
			if err := validateSelectors(t.TempDir()); (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}
