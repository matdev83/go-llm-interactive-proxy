package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_StagedRejectsOverLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 3)
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, os.Getenv)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "3 modified Go files") || !strings.Contains(stderr.String(), "2-file") {
		t.Fatalf("reject message missing counts: %q", stderr.String())
	}
}

func TestRun_StagedIgnoresNonGoPaths(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	for _, name := range []string{"README.md", "notes.txt", "config.json"} {
		path := filepath.Join(repo, name)
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "1"}, &stderr, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
}

func TestRun_StagedAllowsAtLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 2)
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
}

func TestRun_EnvOverrideAllowsOverLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 3)
	getenv := func(key string) string {
		if key == OverrideEnv {
			return "1"
		}
		return ""
	}
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, getenv)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), OverrideEnv) {
		t.Fatalf("override warning missing env name: %q", stderr.String())
	}
}

func TestRun_GitConfigOverrideAllowsOverLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 3)
	git(t, repo, "config", OverrideGitConfig, "true")
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), OverrideGitConfig) {
		t.Fatalf("override warning missing git config key: %q", stderr.String())
	}
}

func TestRun_RangeCountsCommits(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 1)
	git(t, repo, "-c", "user.email=qa@example.com", "-c", "user.name=QA", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := strings.TrimSpace(string(git(t, repo, "rev-parse", "HEAD")))
	for i := range 3 {
		path := filepath.Join(repo, fmt.Sprintf("extra-%d.go", i))
		if err := os.WriteFile(path, []byte("y\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.email=qa@example.com", "-c", "user.name=QA", "-c", "commit.gpgsign=false", "commit", "-qm", "more")
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--base", base, "--head", "HEAD", "--limit", "2"}, &stderr, func(string) string { return "" })
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, stderr.String())
	}
}

func TestRun_UsageError(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	if code := run([]string{"--staged", "--base", "HEAD"}, &stderr, os.Getenv); code != 2 {
		t.Fatalf("mixed flags exit=%d, want 2; stderr=%q", code, stderr.String())
	}
}

func initTempRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git(t, dir, "-c", "core.autocrlf=false", "init", "-q")
	return dir
}

func stageFiles(t *testing.T, repo string, n int) {
	t.Helper()
	for i := range n {
		path := filepath.Join(repo, fmt.Sprintf("f-%02d.go", i))
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
}

func git(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}
