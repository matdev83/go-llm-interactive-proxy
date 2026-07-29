package archtest

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type goListCacheEntry struct {
	done chan struct{}
	out  []byte
	err  error
}

var (
	goListCacheMu sync.Mutex
	goListCache   = make(map[string]*goListCacheEntry)
)

// cachedGoList coalesces identical package-graph queries made by parallel
// architecture tests. The go command cache does not avoid subprocess startup
// and graph loading, which dominate this package's runtime on Windows.
func cachedGoList(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	key := strings.Join(args, "\x00")

	goListCacheMu.Lock()
	if entry, ok := goListCache[key]; ok {
		goListCacheMu.Unlock()
		<-entry.done
		return entry.out, entry.err
	}
	entry := &goListCacheEntry{done: make(chan struct{})}
	goListCache[key] = entry
	goListCacheMu.Unlock()

	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = repoRoot(t)
	entry.out, entry.err = cmd.Output()
	close(entry.done)
	return entry.out, entry.err
}
