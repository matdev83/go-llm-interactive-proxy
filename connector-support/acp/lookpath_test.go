package acp

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestExecutableCache_ResetClearsCachedEntries(t *testing.T) {
	t.Parallel()

	cache := &ExecutableCache{}
	_, _ = cache.LookPath("acp-lookpath-missing-before-reset")

	before := 0
	cache.m.Range(func(_, _ any) bool {
		before++
		return true
	})
	if before == 0 {
		t.Fatal("expected at least one cached entry before Reset")
	}

	cache.Reset()

	after := 0
	cache.m.Range(func(_, _ any) bool {
		after++
		return true
	})
	if after != 0 {
		t.Fatalf("after Reset want 0 cached entries, got %d", after)
	}
}

func TestExecutableCache_ResetConcurrentWithLookups(t *testing.T) {
	t.Parallel()

	cache := &ExecutableCache{}
	const goroutines = 64
	const iterations = 400

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("acp-cache-race-%d", id%8)
			for j := 0; j < iterations; j++ {
				switch j % 4 {
				case 0:
					cache.Reset()
				case 1:
					_, _ = cache.LookPath(key)
				case 2:
					_, _ = cache.CheckExecutable(key)
				default:
					cache.Reset()
					_, _ = cache.LookPath(key)
					_, _ = cache.CheckExecutable(filepath.Join("/", key))
				}
			}
		}(i)
	}

	wg.Wait()
}
