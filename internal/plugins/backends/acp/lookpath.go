package acp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var lookPathCache sync.Map // map[string]func() lookPathResult

type lookPathResult struct {
	path string
	err  error
}

// LookPathCached wraps exec.LookPath with a thread-safe, process-lifetime cache
// to avoid repeated expensive filesystem scans. Entries are never invalidated
// in production; Restart the process after PATH changes. Tests that mutate PATH
// must call ResetLookPathCache.
func LookPathCached(file string) (string, error) {
	v, _ := lookPathCache.LoadOrStore(file, sync.OnceValue(func() lookPathResult {
		p, e := exec.LookPath(file)
		return lookPathResult{path: p, err: e}
	}))
	r, ok := v.(func() lookPathResult)
	if !ok {
		return "", fmt.Errorf("internal: lookPath cache value of unexpected type %T for %q", v, file)
	}
	got := r()
	return got.path, got.err
}

// ResetLookPathCache clears cached LookPath results. Tests that mutate PATH
// must call this so subsequent LookPathCached calls observe the new PATH.
func ResetLookPathCache() {
	lookPathCache.Clear()
}

// CheckExecutable verifies if the candidate is a valid executable. If it's an
// absolute path, it checks if the file exists and is not a directory. Otherwise,
// it performs a cached PATH lookup.
func CheckExecutable(candidate string) (string, bool) {
	c := strings.TrimSpace(candidate)
	if c == "" {
		return "", false
	}
	if filepath.IsAbs(c) {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, true
		}
		return "", false
	}
	if resolved, err := LookPathCached(c); err == nil {
		return resolved, true
	}
	return "", false
}
