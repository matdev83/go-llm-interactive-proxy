package archtest

import (
	"os/exec"
	"sort"
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

// goListCacheKey canonicalizes equivalent `go list` argument lists into a
// single key. The leading flags used by these tests are order-independent, so
// they are sorted while positional package/query arguments remain untouched.
func goListCacheKey(args []string) string {
	firstPositional := 0
	for firstPositional < len(args) && strings.HasPrefix(args[firstPositional], "-") {
		firstPositional++
	}
	flags := append([]string(nil), args[:firstPositional]...)
	sort.Strings(flags)
	return strings.Join(append(flags, args[firstPositional:]...), "\x00")
}

func TestGoListCacheKeyCanonicalizesLeadingFlagsOnly(t *testing.T) {
	t.Parallel()

	first := goListCacheKey([]string{"-json", "-test=false", "./internal/core/..."})
	reorderedFlags := goListCacheKey([]string{"-test=false", "-json", "./internal/core/..."})
	if first != reorderedFlags {
		t.Fatalf("equivalent leading flags produced different keys: %q != %q", first, reorderedFlags)
	}

	queriesAB := goListCacheKey([]string{"-json", "./internal/core/...", "./internal/plugins/..."})
	queriesBA := goListCacheKey([]string{"-json", "./internal/plugins/...", "./internal/core/..."})
	if queriesAB == queriesBA {
		t.Fatalf("positional query order was lost: %q", queriesAB)
	}

	flagBeforeQuery := goListCacheKey([]string{"-json", "-test=false", "./internal/core/..."})
	flagAfterQuery := goListCacheKey([]string{"-json", "./internal/core/...", "-test=false"})
	if flagBeforeQuery == flagAfterQuery {
		t.Fatalf("argument after a positional query was incorrectly canonicalized: %q", flagBeforeQuery)
	}
}

// cachedGoList coalesces identical package-graph queries made by parallel
// architecture tests. The go command cache does not avoid subprocess startup
// and graph loading, which dominate this package's runtime on Windows.
func cachedGoList(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	key := goListCacheKey(args)

	goListCacheMu.Lock()
	if entry, ok := goListCache[key]; ok {
		goListCacheMu.Unlock()
		select {
		case <-entry.done:
			return entry.out, entry.err
		case <-t.Context().Done():
			return nil, t.Context().Err()
		}
	}
	entry := &goListCacheEntry{done: make(chan struct{})}
	goListCache[key] = entry
	// Release waiters even if the producer below terminates early (t.Fatal,
	// Goexit, or a panic); otherwise same-key callers would block forever.
	defer close(entry.done)
	goListCacheMu.Unlock()

	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = repoRoot(t)
	entry.out, entry.err = cmd.Output()
	return entry.out, entry.err
}
