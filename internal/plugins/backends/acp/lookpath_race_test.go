package acp

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestResetLookPathCache_concurrentWithLookups guards against reassigning the
// package-global sync.Map under concurrent ResetLookPathCache / LookPathCached /
// CheckExecutable callers (observed under make test-race).
//
//nolint:paralleltest // Exercises the package-global cache reset contract.
func TestResetLookPathCache_concurrentWithLookups(t *testing.T) {
	t.Cleanup(ResetLookPathCache)

	relMissingProbe := filepath.Join("nonexistent", "lip-lookpath-race-probe-missing")
	const goroutines = 32
	const iters = 200
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range iters {
				ResetLookPathCache()
				_, _ = LookPathCached(relMissingProbe)
				_, _ = CheckExecutable(relMissingProbe)
				_, _ = CheckExecutable("/nonexistent/lip-lookpath-race-abs")
			}
		})
	}
	wg.Wait()
}
