package featurehost

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/reasoningcompose"
)

// ReasoningCompressionOptions is aliased from reasoningcompose for generation options.
type ReasoningCompressionOptions = reasoningcompose.Options

// ReasoningGenerationInput carries generation inputs for reasoning composition.
type ReasoningGenerationInput = reasoningcompose.GenerationInput

// composeReasoningOptions merges production and testing reasoning options.
// It is package-private: only Runtime.CompileGeneration may merge reasoning
// policy, so generic runtimebundle has no separate reasoning entry point
// (Task 2.4, Requirements 8.3/8.4).
func composeReasoningOptions(prod, test ReasoningCompressionOptions) ReasoningCompressionOptions {
	return reasoningcompose.ComposeOptions(prod, test)
}
