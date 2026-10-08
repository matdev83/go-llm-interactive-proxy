// Command devcheck provides explicit, measured development checks. It delegates
// correctness and caching to the toolchain; it never memoizes successful checks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/tools/devcheck/internal/testscope"
)

type testStats struct {
	Passed  int `json:"passed"`
	Cached  int `json:"cached"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	task := flag.String("task", "doctor", "test, build, lint, doctor, or quarantine")
	module := flag.String("module", ".", "repository-relative Go module directory")
	packages := flag.String("packages", "", "explicit space-separated package patterns, e.g. ./pkg/lipapi")
	var skipTest explicitTestSkipFlag
	flag.Var(&skipTest, "skip-test", "skip one exact top-level Test in an explicit task=test run")
	jobs := flag.Int("jobs", min(4, runtime.GOMAXPROCS(0)), "Go package and analyzer concurrency")
	fresh := flag.Bool("fresh", false, "disable test result reuse for a deliberate fresh execution")
	repeat := flag.Int("repeat", 1, "repeat identical checks to distinguish warm reuse from execution cost")
	scope := flag.String("scope", "explicit", "explicit or changed local test scope")
	base := flag.String("base", "", "changed-scope comparison reference (default origin/main)")
	planOnly := flag.Bool("plan", false, "print changed-scope plan without running tests")
	full := flag.Bool("full", false, "run all maintained modules' default tests instead of selecting")
	flag.Parse()
	if flag.NArg() != 0 || *jobs < 1 || *repeat < 1 {
		return errors.New("jobs/repeat must be positive; use named flags for scope")
	}
	if err := validateExplicitTestSkip(
		skipTest.set, skipTest.name, *task, *scope, *full, *base, *planOnly,
	); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	quarantine, err := loadQuarantine(root)
	if err != nil {
		return err
	}
	if *task == "quarantine" {
		if err := checkQuarantine(root, quarantine); err != nil {
			return err
		}
		fmt.Println(skipPattern(quarantine))
		return nil
	}
	if *scope == "changed" {
		if err := validateChangedScope(*task, *module, *packages); err != nil {
			return err
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		plan, err := testscope.Build(ctx, root, testscope.Options{Base: *base, Full: *full})
		cancel()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "planning_elapsed=%.3fs\n", time.Since(start).Seconds())
		return runTestPlan(root, plan, testPlanOptions{jobs: *jobs, repeat: *repeat, fresh: *fresh, dry: *planOnly, quarantine: quarantine}, os.Stdout, os.Stderr)
	}
	if *scope != "explicit" || *base != "" || *planOnly || *full {
		return errors.New("base/plan/full require -scope=changed; scope must be explicit or changed")
	}
	dir, err := moduleDirectory(root, *module)
	if err != nil {
		return err
	}
	if *task == "doctor" {
		return doctor(dir)
	}
	command, err := commandFor(*task, *packages, *jobs, *fresh)
	if err != nil {
		return err
	}
	if skipTest.set {
		goFlags, err := effectiveGOFlags(dir, append(os.Environ(), "GOWORK=off"))
		if err != nil {
			return err
		}
		pattern, err := combinedTestSkipPattern(goFlags, quarantine, skipTest.name)
		if err != nil {
			return fmt.Errorf("combine test skip patterns: %w", err)
		}
		command = withExplicitTestSkip(command, pattern)
		fmt.Fprintf(
			os.Stderr,
			"Excluding top-level test %s from this development run.\n",
			skipTest.name,
		)
	} else {
		command = withQuarantine(command, quarantine)
	}
	// Do not substitute staticcheck for the mandatory multi-linter gate, or
	// report success when the requested analyzer is absent.
	if _, err := exec.LookPath(command[0]); err != nil {
		return fmt.Errorf("required tool %s is unavailable: %w", command[0], err)
	}
	fmt.Fprintln(os.Stderr, "Development feedback only; delivery still requires the applicable comprehensive gates.")
	for n := 1; n <= *repeat; n++ {
		fmt.Fprintf(os.Stderr, "[%d/%d] module=%s command=%s\n", n, *repeat, *module, strings.Join(command, " "))
		start := time.Now()
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stderr = os.Stderr
		stats, err := execute(cmd, *task == "test", os.Stdout)
		fmt.Fprintf(os.Stderr, "elapsed=%.3fs passed=%d cached=%d failed=%d skipped=%d\n", time.Since(start).Seconds(), stats.Passed, stats.Cached, stats.Failed, stats.Skipped)
		if err != nil {
			return fmt.Errorf("%s failed: %w", *task, err)
		}
	}
	return nil
}

func execute(cmd *exec.Cmd, test bool, output io.Writer) (testStats, error) {
	if !test {
		cmd.Stdout = output
		return testStats{}, cmd.Run()
	}
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return testStats{}, err
	}
	if err := cmd.Start(); err != nil {
		return testStats{}, err
	}
	stats, parseErr := consumeTests(stream, output)
	// Always drain and reap even if the output format is invalid.
	if parseErr != nil {
		_, _ = io.Copy(io.Discard, stream)
	}
	return stats, errors.Join(parseErr, cmd.Wait())
}

func commandFor(task, scope string, jobs int, fresh bool) ([]string, error) {
	packages := strings.Fields(scope)
	if len(packages) == 0 {
		return nil, errors.New("explicit scope required: set PKGS='./path/to/package/...' (use ./... deliberately for the full module)")
	}
	for _, p := range packages {
		if p != "." && !strings.HasPrefix(p, "./") {
			return nil, fmt.Errorf("package %q must be relative to MODULE", p)
		}
		if slices.Contains(strings.Split(strings.ReplaceAll(p, "\\", "/"), "/"), "..") {
			return nil, fmt.Errorf("package %q escapes the selected module", p)
		}
	}
	var command []string
	switch task {
	case "test":
		command = []string{"go", "test", "-mod=readonly", "-p=" + strconv.Itoa(jobs), "-parallel=" + strconv.Itoa(jobs), "-timeout=10m", "-json"}
		if fresh {
			command = append(command, "-count=1")
		}
	case "build":
		// Local development binaries do not need a new link solely because
		// another commit changed the embedded VCS revision. Release gates keep
		// their existing stamping and trimpath flags.
		command = []string{"go", "build", "-mod=readonly", "-p=" + strconv.Itoa(jobs), "-buildvcs=false"}
	case "lint":
		command = []string{"golangci-lint", "run", "--allow-parallel-runners", "--concurrency=" + strconv.Itoa(jobs), "--disable=modernize,paralleltest,thelper"}
	default:
		return nil, fmt.Errorf("unknown task %q", task)
	}
	return append(command, packages...), nil
}

func moduleDirectory(root, module string) (string, error) {
	if filepath.IsAbs(module) {
		return "", errors.New("MODULE must be relative to the repository root")
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(root, module))
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("MODULE must stay within the repository")
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return "", fmt.Errorf("MODULE must contain go.mod: %w", err)
	}
	return dir, nil
}

func consumeTests(input io.Reader, output io.Writer) (testStats, error) {
	var stats testStats
	decoder := json.NewDecoder(input)
	for {
		var event struct {
			Action, Package, Test, Output string
		}
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return stats, nil
			}
			return stats, fmt.Errorf("read test telemetry: %w", err)
		}
		if _, err := io.WriteString(output, event.Output); err != nil {
			return stats, err
		}
		if event.Test != "" || event.Package == "" {
			continue
		}
		switch event.Action {
		case "output":
			if strings.HasPrefix(event.Output, "ok\t") || strings.HasPrefix(event.Output, "ok ") {
				if strings.Contains(event.Output, "(cached)") {
					stats.Cached++
				}
			}
		case "pass":
			stats.Passed++
		case "fail":
			stats.Failed++
		case "skip":
			stats.Skipped++
		}
	}
}

func doctor(dir string) error {
	fmt.Printf("module=%s logical_cpus=%d effective_gomaxprocs=%d\n", dir, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	cmd := exec.Command("go", "env", "-json", "GOVERSION", "GOTOOLCHAIN", "GOOS", "GOARCH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOWORK", "CGO_ENABLED", "GOPROXY")
	cmd.Dir = dir
	data, err := cmd.Output()
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		path := env[key]
		if path == "" || path == "off" {
			return fmt.Errorf("%s is disabled; restore a stable writable cache", key)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		probe, err := os.CreateTemp(path, "devcheck-probe-*")
		if err != nil {
			return fmt.Errorf("%s is not writable: %w", key, err)
		}
		if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
			return err
		}
	}
	for _, key := range []string{"GOMAXPROCS", "GODEBUG", "GOLANGCI_LINT_CACHE", "LIP_LINT_JOBS", "LIP_LINT_CONCURRENCY"} {
		fmt.Printf("%s=%s\n", key, os.Getenv(key))
	}
	if linter, err := exec.LookPath("golangci-lint"); err == nil {
		for _, args := range [][]string{{"version"}, {"cache", "status"}} {
			lint := exec.Command(linter, args...)
			lint.Dir = dir
			output, err := lint.CombinedOutput()
			fmt.Print(string(output))
			if err != nil {
				fmt.Printf("lint diagnostic unavailable: %v\n", err)
			}
		}
	} else {
		fmt.Println("golangci-lint is unavailable; dev-lint will fail until installed.")
	}
	fmt.Println("Keep cache paths stable across sessions/worktrees. Compare identical -repeat=2 checks before cleaning anything.")
	fmt.Println("-count=1 reruns tests; -a forces compilation. Race, coverage, tags, trimpath, and toolchain changes create distinct build variants.")
	fmt.Println("For unexplained misses: GODEBUG=gocachetest=1 go test ./path/to/package. For recompilation: go build -x ./path/to/package.")
	return nil
}
