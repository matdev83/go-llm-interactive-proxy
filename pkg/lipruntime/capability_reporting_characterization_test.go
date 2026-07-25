package lipruntime_test

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipruntime"
)

// TestCapabilityReporting_DogfoodFacadeHostSnapshot freezes externally observable
// runtime readiness on the slim public facade path for the standard dogfood composition.
func TestCapabilityReporting_DogfoodFacadeHostSnapshot(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "config", "examples", "dogfood-local-stub.yaml")
	ctx := context.Background()
	rt, err := lipruntime.Build(ctx, lipruntime.Options{ConfigPath: path, LogWriter: io.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	if !rt.Ready() {
		t.Fatal("Ready must be true with active generation")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView required for capability-bearing runtime")
	}
	if st := rt.ReloadStatus(); st.ActiveGeneration < 1 {
		t.Fatalf("active generation=%d want >= 1", st.ActiveGeneration)
	}
}
