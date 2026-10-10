// Command worktreeclean plans or applies cleanup of one explicitly selected,
// delivered task worktree. Remote branches are never removed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/gitscope"
	"github.com/matdev83/go-llm-interactive-proxy/tools/taskrunner"
)

type snapshot struct {
	Path          string   `json:"path"`
	Branch        string   `json:"branch"`
	Head          string   `json:"head"`
	PublishedHead string   `json:"published_head"`
	State         string   `json:"pr_state"`
	Merge         string   `json:"merge_commit"`
	Dirty         string   `json:"dirty,omitempty"`
	Ignored       []string `json:"ignored,omitempty"`
	Users         []string `json:"active_users,omitempty"`
}

func main() {
	path := flag.String("path", "", "exact absolute selected worktree")
	branch := flag.String("branch", "", "exact local task branch")
	pr := flag.Int("pr", 0, "merged PR proving delivery")
	apply := flag.Bool("apply", false, "apply the validated plan (default dry run)")
	discardIndex := flag.Bool("discard-codegraph", false, "explicitly allow discarding only ignored .codegraph generated files")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cleanup(ctx, *path, *branch, *pr, *apply, *discardIndex); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func query(ctx context.Context, dir string, args ...string) (string, error) {
	data, err := taskrunner.Output(ctx, taskrunner.Request{Argv: args, Dir: dir, Env: gitscope.Environ(), ClearEnv: true, Timeout: time.Minute})
	return string(data), err
}

func selectedPath(container, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("cleanup requires an exact absolute path")
	}
	worktrees := filepath.Join(container, "worktrees")
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(worktrees)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("target must be a task directory inside container/worktrees, never a receiver or container")
	}
	return real, nil
}

func safeSnapshot(state snapshot, discardIndex bool) error {
	if state.Dirty != "" {
		return errors.New("tracked/untracked work exists; cleanup refused")
	}
	for _, name := range state.Ignored {
		if !discardIndex || !strings.HasPrefix(filepath.ToSlash(name), ".codegraph/") {
			return fmt.Errorf("ignored work must be preserved: %s", name)
		}
	}
	if len(state.Users) > 0 {
		return fmt.Errorf("worktree has active users: %v", state.Users)
	}
	if state.Head == "" || state.Head != state.PublishedHead || state.State != "MERGED" {
		return errors.New("selected tip is not the published head of a merged PR")
	}
	return nil
}

func inspect(ctx context.Context, repo, path, branch string, pr int) (snapshot, error) {
	state := snapshot{Path: path, Branch: branch}
	common, err := query(ctx, path, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return state, err
	}
	repoCommon, err := query(ctx, repo, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return state, err
	}
	if strings.TrimSpace(common) != strings.TrimSpace(repoCommon) {
		return state, errors.New("target belongs to another repository")
	}
	worktrees, err := query(ctx, repo, "git", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return state, err
	}
	found := false
	for field := range strings.SplitSeq(worktrees, "\x00") {
		if candidate, ok := strings.CutPrefix(field, "worktree "); ok {
			real, err := filepath.EvalSymlinks(candidate)
			if err == nil && real == path {
				found = true
			}
		}
	}
	if !found {
		return state, errors.New("selected path is not a registered worktree")
	}
	actual, err := query(ctx, path, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(actual) != branch {
		return state, errors.New("selected worktree does not hold the exact requested branch")
	}
	head, err := query(ctx, path, "git", "rev-parse", "HEAD")
	if err != nil {
		return state, err
	}
	state.Head = strings.TrimSpace(head)
	dirty, err := query(ctx, path, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return state, err
	}
	state.Dirty = strings.TrimSpace(dirty)
	ignored, err := query(ctx, path, "git", "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return state, err
	}
	for name := range strings.SplitSeq(ignored, "\x00") {
		if name != "" {
			state.Ignored = append(state.Ignored, name)
		}
	}
	state.Users, err = activeUsers(ctx, path)
	if err != nil {
		return state, err
	}
	data, err := query(ctx, repo, "gh", "pr", "view", strconv.Itoa(pr), "--json", "state,headRefName,headRefOid,mergeCommit")
	if err != nil {
		return state, err
	}
	var delivery struct {
		State, HeadRefName, HeadRefOid string
		MergeCommit                    struct{ OID string }
	}
	if err := json.Unmarshal([]byte(data), &delivery); err != nil {
		return state, err
	}
	if delivery.HeadRefName != branch {
		return state, errors.New("PR branch does not match the explicitly selected local branch")
	}
	state.State, state.PublishedHead, state.Merge = delivery.State, delivery.HeadRefOid, delivery.MergeCommit.OID
	return state, nil
}

func activeUsers(ctx context.Context, path string) ([]string, error) {
	var users []string
	if runtime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, err := strconv.Atoi(entry.Name()); err != nil {
				continue
			}
			status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			owned := false
			zombie := false
			for line := range strings.SplitSeq(string(status), "\n") {
				if strings.HasPrefix(line, "State:") {
					fields := strings.Fields(line)
					zombie = len(fields) > 1 && fields[1] == "Z"
				}
				if !strings.HasPrefix(line, "Uid:") {
					continue
				}
				for _, uid := range strings.Fields(line)[1:] {
					if uid == strconv.Itoa(os.Geteuid()) {
						owned = true
					}
				}
			}
			if !owned || zombie {
				continue
			}
			cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				// Inaccessible same-user processes cannot be ruled out.
				return nil, fmt.Errorf("inspect process %s cwd: %w", entry.Name(), err)
			}
			if cwd == path || strings.HasPrefix(cwd, path+string(filepath.Separator)) {
				users = append(users, "pid "+entry.Name())
			}
		}
		return users, nil
	}
	if runtime.GOOS == "darwin" {
		data, err := query(ctx, ".", "lsof", "-a", "-u", strconv.Itoa(os.Geteuid()), "-Fpn", "-d", "cwd")
		if err != nil {
			return nil, err
		}
		pid := ""
		for line := range strings.SplitSeq(data, "\n") {
			if strings.HasPrefix(line, "p") {
				pid = line[1:]
			}
			if strings.HasPrefix(line, "n") {
				cwd := line[1:]
				if cwd == path || strings.HasPrefix(cwd, path+string(filepath.Separator)) {
					users = append(users, "pid "+pid)
				}
			}
		}
		return users, nil
	}
	return nil, errors.New("active-user inspection is unsupported on this platform; cleanup refused")
}

func cleanup(ctx context.Context, path, branch string, pr int, apply, discardIndex bool) error {
	if path == "" || branch == "" || branch == "main" || branch == "dev" || strings.HasPrefix(branch, "-") || pr < 1 {
		return errors.New("exact path/task branch and merged PR number are required")
	}
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	common, err := query(ctx, repo, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	parent := filepath.Dir(strings.TrimSpace(common))
	if filepath.Base(filepath.Dir(parent)) != "branches" {
		return errors.New("repository is not in the managed container layout")
	}
	container := filepath.Dir(filepath.Dir(parent))
	path, err = selectedPath(container, path)
	if err != nil {
		return err
	}
	state, err := inspect(ctx, repo, path, branch, pr)
	if err != nil {
		return err
	}
	if err := safeSnapshot(state, discardIndex); err != nil {
		return err
	}
	if state.Merge == "" {
		return errors.New("merged PR has no merge commit identity")
	}
	if _, err := query(ctx, repo, "git", "merge-base", "--is-ancestor", state.Merge, "origin/main"); err != nil {
		return errors.New("fetch main first: PR merge is not present in local origin/main")
	}
	base, err := query(ctx, repo, "git", "merge-base", state.Head, state.Merge+"^")
	if err != nil {
		return err
	}
	paths, err := query(ctx, repo, "git", "diff", "--name-only", "-z", strings.TrimSpace(base), state.Head, "--")
	if err != nil {
		return err
	}
	args := []string{"git", "diff", "--exit-code", state.Head, state.Merge, "--"}
	for name := range strings.SplitSeq(paths, "\x00") {
		if name != "" {
			args = append(args, name)
		}
	}
	if len(args) == 6 {
		return errors.New("no unique delivered paths found; explicit review required")
	}
	if _, err := query(ctx, repo, args...); err != nil {
		return errors.New("unique changed paths do not match the delivered merge")
	}
	if err := json.NewEncoder(os.Stdout).Encode(state); err != nil {
		return err
	}
	if !apply {
		fmt.Println("dry run: remove selected worktree and local branch only; remote branch preserved")
		return nil
	}
	fresh, err := inspect(ctx, repo, path, branch, pr)
	if err != nil {
		return err
	}
	if err := safeSnapshot(fresh, discardIndex); err != nil {
		return err
	}
	if !reflect.DeepEqual(state, fresh) {
		return errors.New("worktree/delivery state changed during cleanup planning")
	}
	if _, err := query(ctx, repo, "git", "worktree", "remove", path); err != nil {
		return err
	}
	registered, probeErr := query(ctx, repo, "git", "worktree", "list", "--porcelain", "-z")
	if probeErr != nil || strings.Contains(registered, "branch refs/heads/"+branch+"\x00") {
		return fmt.Errorf("worktree removed, local branch retained because ownership is uncertain: %w", errors.Join(errors.New("cannot confirm branch is unused"), probeErr))
	}
	// Squash delivery need not preserve ancestry. CAS deletion refuses any
	// concurrent branch move, even when the replacement tip is fully merged.
	if _, err := query(ctx, repo, "git", "update-ref", "-d", "refs/heads/"+branch, state.Head); err != nil {
		return fmt.Errorf("worktree removed, local branch retained: %w", err)
	}
	fmt.Println("selected worktree and local branch removed; remote branch preserved")
	return nil
}
