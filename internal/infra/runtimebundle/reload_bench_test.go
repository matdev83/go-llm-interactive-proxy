package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// BenchmarkCandidateCompilation measures the real production generation
// compiler, including standard plugin/frontend/feature construction. Candidate
// resources are rolled back after every iteration, as check-config would do.
func BenchmarkCandidateCompilation(b *testing.B) {
	res, err := runtimebundle.BuildBootstrap(b.Context(), runtimebundle.BuildBootstrapInput{
		ConfigPath:      filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeRequestPlane,
	})
	if err != nil {
		b.Fatalf("bootstrap: %v", err)
	}
	b.Cleanup(func() {
		if res.GenerationManager != nil {
			_ = res.GenerationManager.ShutdownDetached(context.Background(), runtimehost.NewLifecycleWorker())
		}
		if res.ProcessServices != nil {
			_ = res.ProcessServices.Close()
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	})
	compiler := runtimebundle.GenerationCompiler{
		Process: res.ProcessServices,
		Compose: stdhttp.ComposeRequestPlane,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		plane, err := compiler.Compile(b.Context(), res.Config, nil)
		if err != nil {
			b.Fatalf("compile: %v", err)
		}
		if err := plane.Close(); err != nil {
			b.Fatalf("rollback: %v", err)
		}
	}
}
