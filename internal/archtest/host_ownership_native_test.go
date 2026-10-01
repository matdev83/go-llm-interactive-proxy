package archtest

import (
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

func nativeArchBuildContexts() []archBuildContext {
	return []archBuildContext{{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
}

func TestBuildHostIsSoleBindHostCaller_Native(t *testing.T) {
	t.Parallel()
	callers, analyzed := bindHostCallersInContexts(t, nativeArchBuildContexts(), nil)
	sort.Strings(callers)
	if len(callers) != 1 || callers[0] != "host_build.go:buildHost" {
		t.Fatalf("bindHost caller graph must be exactly [host_build.go:buildHost], got %v", callers)
	}
	assertNativeProductionGoInventory(t, runtimebundleDir(t), analyzed)
}

func TestBuildHostIsSoleBindHostCaller_NativeOverlaySecondCallerDetected(t *testing.T) {
	t.Parallel()
	path := filepath.Join(runtimebundleDir(t), "bindhost_native_overlay.go")
	overlay := map[string][]byte{path: []byte(`package runtimebundle

func rogueNativeBindHostCaller() (*Host, error) {
	return bindHost("native-overlay", bindHostInput{})
}
`)}
	callers, analyzed := bindHostCallersInContexts(t, nativeArchBuildContexts(), overlay)
	if !slices.Contains(callers, "bindhost_native_overlay.go:rogueNativeBindHostCaller") {
		t.Fatalf("native overlay second bindHost caller must be detected; callers=%v", callers)
	}
	if !analyzed[filepath.Clean(path)] {
		t.Fatal("native overlay missing from analyzed inventory")
	}
}

func TestRuntimehostOwnership_NativeCallerGraph(t *testing.T) {
	assertRuntimehostOwnershipForContexts(t, nativeArchBuildContexts())
}

func TestRuntimehostOwnership_NativeRogueConstructorCallerDetected(t *testing.T) {
	assertRuntimehostRogueConstructorForContexts(t, nativeArchBuildContexts())
}

func assertNativeProductionGoInventory(t *testing.T, dir string, analyzed map[string]bool) {
	t.Helper()
	context := build.Default
	context.CgoEnabled = false
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		matched, err := context.MatchFile(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		if matched && !analyzed[filepath.Clean(filepath.Join(dir, name))] {
			t.Fatalf("native production file missing from analyzed inventory: %s", name)
		}
	}
}
