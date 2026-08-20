package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, os.Getenv))
}

func run(args []string, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("changesize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "git repository root")
	staged := fs.Bool("staged", false, "count staged index paths (pre-commit)")
	base := fs.String("base", "", "diff range base revision")
	head := fs.String("head", "", "diff range head revision")
	limit := fs.Int("limit", DefaultLimit, "maximum modified Go files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "usage: changesize [--staged | --base <rev> --head <rev>] [--limit %d] [--repo <dir>]\n", DefaultLimit)
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "check-change-size: --limit must be >= 1")
		return 2
	}

	useStaged := *staged
	if !useStaged && *base == "" && *head == "" {
		useStaged = true
	}
	if useStaged && (*base != "" || *head != "") {
		fmt.Fprintln(stderr, "check-change-size: use --staged or --base/--head, not both")
		return 2
	}
	if !useStaged && (*base == "" || *head == "") {
		fmt.Fprintln(stderr, "check-change-size: --base and --head are required together")
		return 2
	}

	var gitArgs []string
	if useStaged {
		gitArgs = []string{"diff", "--cached", "--name-only", "--diff-filter=ACDMRT", "-z"}
	} else {
		gitArgs = []string{"diff", "--name-only", "--diff-filter=ACDMRT", "-z", *base + "..." + *head}
	}
	out, err := gitOutput(*repo, gitArgs...)
	if err != nil {
		fmt.Fprintf(stderr, "check-change-size: git diff: %v\n", err)
		return 2
	}
	count := uniqueGoPathCount(splitGitNames(out))

	envOverride := truthy(getenv(OverrideEnv))
	gitOverride := gitConfigBool(*repo, OverrideGitConfig)
	override := envOverride || gitOverride

	if allowed(count, *limit, override) {
		if count > *limit && override {
			reason := OverrideGitConfig
			if envOverride {
				reason = OverrideEnv
			}
			fmt.Fprintf(stderr, "check-change-size: %d modified Go files exceed the %d-file limit; proceeding because %s is set.\n", count, *limit, reason)
		}
		return 0
	}

	fmt.Fprintf(stderr, "check-change-size: %d modified Go files exceed the %d-file limit.\n", count, *limit)
	fmt.Fprintf(stderr, "Split the change into smaller reviewable PRs/commits, or set %s=1 or `git config %s true` (admin override).\n", OverrideEnv, OverrideGitConfig)
	return 1
}

func gitOutput(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

func gitConfigBool(repo, key string) bool {
	out, err := gitOutput(repo, "config", "--bool", "--get", key)
	if err != nil {
		return false
	}
	return truthy(string(out))
}
