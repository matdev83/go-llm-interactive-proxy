package featurehost_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestReasoning_ComposeOptions(t *testing.T) {
	t.Parallel()

	rt, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger: nilDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	// Production and testing sources merge inside CompileGeneration with
	// production precedence; the merge itself has no separate entry point.
	out, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
		ReasoningProdOpts: featurehost.ReasoningCompressionOptions{},
		ReasoningTestOpts: featurehost.ReasoningCompressionOptions{},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if !out.Planes.IsZero() {
		t.Fatal("expected zero planes preserved through no-op reasoning composition")
	}
}

func TestReasoning_ValidateAndBind(t *testing.T) {
	t.Parallel()

	rt, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{
		Logger: nilDiscardLogger(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	var regs []lipsdk.Registration

	merged := featurebundle.GeneratedMergeSurface{}
	out, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations:     regs,
		MergeSurface:      merged,
		Planes:            merged.Frozen,
		Lifecycles:        merged.Lifecycles,
		ReasoningProdOpts: featurehost.ReasoningCompressionOptions{},
		ReasoningTestOpts: featurehost.ReasoningCompressionOptions{},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if out.Planes.IsZero() != merged.Frozen.IsZero() {
		t.Fatal("unexpected frozen plane state")
	}
}
