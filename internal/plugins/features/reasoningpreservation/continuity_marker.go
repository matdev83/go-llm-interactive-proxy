package reasoningpreservation

import (
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func containsDialect(dialects []lipapi.ReasoningDialect, want lipapi.ReasoningDialect) bool {
	want = lipapi.NormalizeReasoningDialect(want)
	return slices.Contains(lipapi.NormalizeReasoningDialects(dialects), want)
}
