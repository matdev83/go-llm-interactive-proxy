//go:build red

package runtimebundle_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
)

// TestGenerationBundle_AbsentReceiverTerminalDecisionProvider_RED characterizes
// Requirement 1.1 (Task 1.1): invoking TerminalDecisionProvider on an absent (*GenerationBundle)(nil)
// must return a nil terminaldecision.Provider without panicking.
// On the review baseline before Task 2.1, this test fails (panics) because
// (*GenerationBundle)(nil).TerminalDecisionProvider dereferences b.operations.
func TestGenerationBundle_AbsentReceiverTerminalDecisionProvider_RED(t *testing.T) {
	t.Parallel()

	var b *runtimebundle.GenerationBundle
	require.Nil(t, b.TerminalDecisionProvider())
}
