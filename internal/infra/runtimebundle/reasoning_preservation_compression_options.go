package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// ReasoningCompressionOptions holds trusted reasoning semantic compression
// composition seams. Production and testing share the same shape; the
// dedicated file keeps ProductionOptions/TestingOptions compact and preserves
// the reasoning_preservation_compression overlay.
type ReasoningCompressionOptions struct {
	EgressPolicies  map[string]reasoningpreservation.EgressPolicy
	MatcherResolver sdk.MatcherResolver
}
