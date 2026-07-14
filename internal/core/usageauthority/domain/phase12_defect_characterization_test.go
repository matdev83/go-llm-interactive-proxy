package domain_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
)

// Phase 2.1: inferred totals use inclusion schema (cache ⊂ input, reasoning ⊂ output).

func TestInferTotalTokens_doesNotDoubleCountCache(t *testing.T) {
	t.Parallel()

	p := domain.PreflightUsage{
		InputTokens:      100, // includes cache in provider semantics
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		TotalTokens:      0,
		// TotalTokensPresent false → infer
	}
	got, ok := p.AmountForUnit(domain.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) returned false")
	}
	if got.Value != 120 {
		t.Fatalf("total=%d want 120 (input+output; cache is an input subcomponent)", got.Value)
	}
}

func TestInferTotalTokens_doesNotDoubleCountReasoning(t *testing.T) {
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
	if got.Value != 60 {
		t.Fatalf("total=%d want 60 (input+output; reasoning is an output subcomponent)", got.Value)
	}
}

func TestInferTotalTokens_explicitZeroPresentDoesNotReinfer(t *testing.T) {
	t.Parallel()

	p := domain.PreflightUsage{
		InputTokens:        100,
		OutputTokens:       20,
		CacheReadTokens:    40,
		TotalTokens:        0,
		TotalTokensPresent: true,
	}
	got, ok := p.AmountForUnit(domain.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) returned false")
	}
	if got.Value != 0 {
		t.Fatalf("explicit present zero total=%d want 0 (must not re-infer)", got.Value)
	}
}

func TestInferTotalTokens_explicitTotalPresentWins(t *testing.T) {
	t.Parallel()

	p := domain.PreflightUsage{
		InputTokens:        100,
		OutputTokens:       20,
		TotalTokens:        99,
		TotalTokensPresent: true,
	}
	got, ok := p.AmountForUnit(domain.AmountUnitTotalTokens)
	if !ok {
		t.Fatal("AmountForUnit(total) returned false")
	}
	if got.Value != 99 {
		t.Fatalf("total=%d want 99 (present total wins)", got.Value)
	}
}
