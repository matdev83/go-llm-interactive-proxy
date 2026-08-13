package qa

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxDirtyGoFiles = 100

func dirtyGoLimitExceeded(count int) bool {
	return count > maxDirtyGoFiles
}

func listDirtyGoFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git status: %w", err)
	}
	return parsePorcelainGoPaths(out), nil
}

func parsePorcelainGoPaths(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return nil
	}
	fields := bytes.Split(raw, []byte{0})
	seen := make(map[string]struct{})
	var paths []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || !strings.HasSuffix(path, ".go") {
			return
		}
		path = filepath.ToSlash(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 4 || field[2] != ' ' {
			continue
		}
		x, y := field[0], field[1]
		add(string(field[3:]))
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++
			if i < len(fields) {
				add(string(fields[i]))
			}
		}
	}
	return paths
}
