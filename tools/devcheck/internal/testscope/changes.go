package testscope

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

func commandOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func changedPaths(ctx context.Context, root, base string) ([]string, string, error) {
	out, err := commandOutput(ctx, root, "git", "merge-base", "--", base, "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("resolve comparison base: %w", err)
	}
	comparison := strings.TrimSpace(string(out))
	commands := [][]string{
		{"diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRD", comparison, "HEAD", "--"},
		{"status", "--porcelain=v1", "--no-renames", "-z", "--untracked-files=all"},
	}
	var paths []string
	for _, args := range commands {
		out, err := commandOutput(ctx, root, "git", args...)
		if err != nil {
			return nil, comparison, err
		}
		if args[0] == "status" {
			working, err := statusPaths(out)
			if err != nil {
				return nil, comparison, err
			}
			paths = append(paths, working...)
			continue
		}
		for _, name := range strings.Split(string(out), "\x00") {
			if name != "" {
				paths = append(paths, name)
			}
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), comparison, nil
}

func statusPaths(out []byte) ([]string, error) {
	var paths []string
	for _, record := range strings.Split(string(out), "\x00") {
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' || strings.ContainsAny(record[:2], "RC") {
			return nil, fmt.Errorf("unexpected Git status record: %q", record)
		}
		paths = append(paths, record[3:])
	}
	return paths, nil
}
