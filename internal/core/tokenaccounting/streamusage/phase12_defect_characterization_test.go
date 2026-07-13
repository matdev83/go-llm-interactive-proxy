package streamusage

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Phase 1.2 characterization: local totalTokens inference double-counts
// subcomponents when provider totals are absent (F-08).

func TestTotalTokens_currentlyDoubleCountsCacheAndReasoning(t *testing.T) {
	t.Parallel()

	usage := lipapi.ScopedUsageDelta{
		InputTokens:      100,
		OutputTokens:     50,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  15,
	}
	got := totalTokens(usage, 0, 0)
	// Current: sum all fields = 215. Desired with inclusion schema: 150 (100+50).
	if got != 215 {
		t.Fatalf("current defect characterization: totalTokens=%d want 215; Phase 2 must not double-count subcomponents", got)
	}
}

func TestPhase12_desiredStreamTotalInferenceCurrentlyAbsent(t *testing.T) {
	t.Parallel()

	usage := lipapi.ScopedUsageDelta{
		InputTokens:      100,
		OutputTokens:     50,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  15,
	}
	got := totalTokens(usage, 0, 0)
	if got == 150 {
		t.Fatal("desired non-double-count stream total already present; Phase 2 flip due")
	}
}
