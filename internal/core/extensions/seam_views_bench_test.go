package extensions_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
)

// BenchmarkCompletionGatesFromContext_nilFallback_empty measures the hot path where no snapshot is
// on context and fallback is nil — result is the shared empty slice (zero allocations per call).
func BenchmarkCompletionGatesFromContext_nilFallback_empty(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = extensions.CompletionGatesFromContext(ctx, nil)
	}
}

// BenchmarkCompletionGatesFromContext_fallbackNilGates_empty uses a fallback view whose
// CompletionGates returns nil; should hit the same shared empty slice as nil fallback.
func BenchmarkCompletionGatesFromContext_fallbackNilGates_empty(b *testing.B) {
	ctx := context.Background()
	bus := hooks.New(hooks.Config{})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
	b.ReportAllocs()
	for b.Loop() {
		_ = extensions.CompletionGatesFromContext(ctx, fallback)
	}
}

// BenchmarkCompletionGatesFromContext_withGates contrasts allocation behavior when gates exist.
func BenchmarkCompletionGatesFromContext_withGates(b *testing.B) {
	bus := hooks.New(hooks.Config{})
	g := gateID("g")
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		CompletionGates: []completion.Gate{g},
	})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = extensions.CompletionGatesFromContext(ctx, fallback)
	}
}
