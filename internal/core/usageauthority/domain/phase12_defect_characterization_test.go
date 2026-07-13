package domain_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
)

// Phase 1.2 characterization: TotalTokens inference double-counts cache/reasoning
// subcomponents (F-08). Phase 2 flip: inferred total must not add subcomponents
// already included in input/output.

func TestInferTotalTokens_currentlyDoubleCountsCache(t *testing.T) {
	t.Parallel()

	p := domain.PreflightUsage{
		InputTokens:      100, // includes cache in provider semantics
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		TotalTokens:      0,
	}
	got, ok := p.AmountForUnit(domain.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) returned false")
	}
	// Current: 100+20+40+10 = 170 (double-counts cache).
	// Desired (Phase 2): 120 (input already includes cache).
	if got.Value != 170 {
		t.Fatalf("current defect characterization: total=%d want 170 (double-count); Phase 2 must infer 120", got.Value)
	}
}

func TestInferTotalTokens_currentlyDoubleCountsReasoning(t *testing.T) {
	t.Parallel()

	p := domain.PreflightUsage{
		InputTokens:     10,
		OutputTokens:    50, // includes reasoning in provider semantics
		ReasoningTokens: 15,
		TotalTokens:     0,
	}
	got, ok := p.AmountForUnit(domain.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) returned false")
	}
	// Current: 10+50+15 = 75. Desired (Phase 2): 60.
	if got.Value != 75 {
		t.Fatalf("current defect characterization: total=%d want 75 (double-count); Phase 2 must infer 60", got.Value)
	}
}

func TestPhase12_desiredTotalInferenceCurrentlyAbsent(t *testing.T) {
	t.Parallel()

	p := domain.PreflightUsage{
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  5,
		TotalTokens:      0,
	}
	got, _ := p.AmountForUnit(domain.AmountUnitTotalTokens)
	desired := int64(120) // input includes cache; reasoning would need output-includes model — use cache-only case
	_ = desired
	// Desired schema default: input includes cache, output includes reasoning → 100+20=120
	// (reasoning already in output; cache already in input).
	if got.Value == 120 {
		t.Fatal("desired non-double-count total inference already present; Phase 2 flip due")
	}
}
