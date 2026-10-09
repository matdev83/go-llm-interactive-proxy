//go:build linux || darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLI_SummaryPreservesBothStreamsAndVerdict(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name               string
		failure, telemetry bool
	}{
		{name: "success"}, {name: "failure", failure: true}, {name: "test telemetry", telemetry: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "logs", "manifest.json")
			script := "#!/bin/sh\n[ \"$1\" = build ] || exit 0\necho verbose-stdout\necho diagnostic-stderr >&2\n"
			task := "build"
			if scenario.telemetry {
				task = "test"
				if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, quarantineFile), []byte("TestExcluded https://github.com/matdev83/go-llm-interactive-proxy/issues/1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				script = "#!/bin/sh\n[ \"$1\" = test ] || exit 0\nprintf '%s\\n' '{\"Action\":\"output\",\"Package\":\"fixture\",\"Output\":\"verbose-stdout\\n\"}' '{\"Action\":\"pass\",\"Package\":\"fixture\"}'\necho diagnostic-stderr >&2\n"
			}
			if scenario.failure {
				script += "exit 3\n"
			}
			if scenario.telemetry {
				script = strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = env ]; then echo \"$GOFLAGS\"; exit 0; fi\n", 1)
			}
			for name, data := range map[string]string{"go.mod": "module example.invalid/report\n\ngo 1.26.9\n", "go": script} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDevcheckSignalHelper$")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DEVCHECK_CLI_FIXTURE="+path, "DEVCHECK_CLI_OUTPUT=summary", "DEVCHECK_CLI_TASK="+task)
			if scenario.telemetry {
				cmd.Env = append(cmd.Env, "GOFLAGS=-skip=^TestInherited$")
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != scenario.failure {
				t.Fatalf("summary lost verdict: %v\n%s", err, output)
			}
			if strings.Contains(string(output), "verbose-stdout") || strings.Contains(string(output), "diagnostic-stderr") || !strings.Contains(string(output), path) {
				t.Fatalf("summary flooded output or hid evidence location:\n%s", output)
			}
			manifest := readManifest(t, path)
			want := "passed"
			if scenario.failure {
				want = "failed"
			}
			if manifest.Outcome != want || len(manifest.Steps) == 0 {
				t.Fatalf("report verdict=%+v", manifest)
			}
			if scenario.telemetry && (len(manifest.Skips) != 2 || manifest.Skips[0].Target != "^TestInherited$" || manifest.Skips[1].Target != "TestExcluded") {
				t.Fatalf("requested exclusion absent from report: %+v", manifest.Skips)
			}
			for _, step := range manifest.Steps {
				if step.Scope.Module != "." || len(step.Scope.Packages) != 1 || step.Scope.Packages[0] != "./..." {
					t.Fatalf("actual scope not recorded: %+v", step.Scope)
				}
				log, err := os.ReadFile(step.LogPath)
				if err != nil || !strings.Contains(string(log), "verbose-stdout") || !strings.Contains(string(log), "diagnostic-stderr") {
					t.Fatalf("full stdout/stderr log missing: %q, %v", log, err)
				}
				if scenario.telemetry && (!strings.Contains(string(log), `"Action":"pass"`) || step.Tests == nil || step.Tests.Passed != 1) {
					t.Fatalf("raw telemetry/counters lost: %q %+v", log, step.Tests)
				}
			}
		})
	}
}
