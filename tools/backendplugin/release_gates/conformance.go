package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/tools/backendplugin/runner"
	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

// conformanceNameRe matches advertised-capability / parity / describe / configure / inventory / conformance suites.
var conformanceNameRe = regexp.MustCompile(`^Test(Parity_|Conformance_|Describe_|Configure_|Inventory_)`)

type listEvent struct {
	Action  string `json:"Action"`
	Test    string `json:"Test"`
	Package string `json:"Package"`
	Output  string `json:"Output"`
}

var runCommand = runner.Run

// listMatchingTests runs `go test -json -list . ./...` and returns names matching re.
func listMatchingTests(modRoot string, re *regexp.Regexp) ([]string, error) {
	result := runCommand(context.Background(), runner.Request{
		Argv:    []string{"go", "test", "-json", "-list", ".", "./..."},
		Dir:     modRoot,
		Env:     []string{"GOWORK=off"},
		Timeout: 8 * time.Minute,
		Output:  taskrunner.Capture,
		Label:   "release_gates:" + filepath.Base(modRoot) + ":conformance-list",
	})
	out := result.Stdout
	if result.Kind != taskrunner.Success {
		return nil, fmt.Errorf("go test -list: %w", runner.Error(result))
	}
	seen := map[string]struct{}{}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var ev listEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Test != "" && !strings.Contains(ev.Test, "/") && re.MatchString(ev.Test) {
			if _, ok := seen[ev.Test]; !ok {
				seen[ev.Test] = struct{}{}
				names = append(names, ev.Test)
			}
			continue
		}
		if ev.Action == "output" && ev.Output != "" {
			line := strings.TrimSpace(ev.Output)
			if strings.HasPrefix(line, "Test") && !strings.Contains(line, " ") && re.MatchString(line) {
				if _, ok := seen[line]; !ok {
					seen[line] = struct{}{}
					names = append(names, line)
				}
			}
		}
	}
	return names, nil
}

// countJSONTestRuns counts Action=="run" events for leaf tests under -run pattern.
func countJSONTestRuns(out []byte) int {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var ev listEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Action == "run" && ev.Test != "" && !strings.Contains(ev.Test, "/") {
			n++
		}
	}
	return n
}

func runConformanceFilter(modRoot string) (step string, matched int, err error) {
	names, err := listMatchingTests(modRoot, conformanceNameRe)
	if err != nil {
		return "conformance_filter:fail", 0, err
	}
	if len(names) == 0 {
		return "conformance_filter:skip_no_tests", 0, fmt.Errorf("no advertised-capability conformance tests discovered")
	}
	// Run only discovered names joined by | (anchored by go test -run regex).
	pat := "^(" + strings.Join(quoteAlternation(names), "|") + ")$"
	result := runCommand(context.Background(), runner.Request{
		Argv:    []string{"go", "test", "-json", "-count=1", "-timeout=15m", "./...", "-run", pat},
		Dir:     modRoot,
		Env:     []string{"GOWORK=off"},
		Timeout: 15 * time.Minute,
		Output:  taskrunner.Capture,
		Label:   "release_gates:" + filepath.Base(modRoot) + ":conformance",
	})
	out := result.Stdout
	matched = countJSONTestRuns(out)
	if result.Kind != taskrunner.Success {
		return "conformance_filter:fail", matched, runner.Error(result)
	}
	if matched == 0 {
		return "conformance_filter:skip_no_tests", 0, fmt.Errorf("conformance filter matched zero test runs")
	}
	return "conformance_filter:ok", matched, nil
}

func quoteAlternation(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = regexp.QuoteMeta(n)
	}
	return out
}

// goTestListHasMatches returns whether `go test -list regexp` in pkg yields any Test lines.
func goTestListHasMatches(root, pkg, pattern string) (int, error) {
	result := runCommand(context.Background(), runner.Request{
		Argv:    []string{"go", "test", "-list", pattern, pkg},
		Dir:     root,
		Timeout: 8 * time.Minute,
		Output:  taskrunner.Capture,
		Label:   "release_gates:selector:" + pkg,
	})
	out := result.Stdout
	if result.Kind != taskrunner.Success {
		return 0, fmt.Errorf("go test -list %s %s: %w", pattern, pkg, runner.Error(result))
	}
	n := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Test") {
			n++
		}
	}
	return n, nil
}

func hostGOOS() string { return runtime.GOOS }

func absJoin(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}
