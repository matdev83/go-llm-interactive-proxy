package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

// BenchmarkCandidateCompilation measures the real production generation
// compiler, including standard plugin/frontend/feature construction. Candidate
// resources are rolled back after every iteration, as check-config would do.
func BenchmarkCandidateCompilation(b *testing.B) {
	host, err := runtimebundle.BuildHost(b.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		b.Fatalf("BuildHost: %v", err)
	}
	b.Cleanup(func() { _ = host.Close(context.Background()) })
	compiler := runtimebundle.GenerationCompiler{
		Process: host.Process,
		Compose: stdhttp.ComposeStandardHTTP,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		plane, err := compiler.Compile(b.Context(), host.Config, nil)
		if err != nil {
			b.Fatalf("compile: %v", err)
		}
		if err := plane.Close(); err != nil {
			b.Fatalf("rollback: %v", err)
		}
	}
}
