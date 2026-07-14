package runtime

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// Phase 2.3: spend uses resolved unknown-output bounds; totals keep inclusion schema.

func phase12SpendCatalog(t *testing.T) accounting.PriceCatalog {
	t.Helper()
	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "phase12-spend",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:     "backend-1",
			Model:       "model-1",
			InputPer1M:  "1.00",
			OutputPer1M: "2.00",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// TestAttemptAuthoritySpendAmount_usesAdjustedMaxWhenCountOutputZero proves F-03 fix:
// omitted max is resolved via AdjustedMaxOutputTokens / Count.OutputTokens, not zero exposure.
func TestAttemptAuthoritySpendAmount_usesAdjustedMaxWhenCountOutputZero(t *testing.T) {
	t.Parallel()

	catalog := phase12SpendCatalog(t)
	cand := authorityCandidate()
	adjusted := 1_000_000
	decision := accountingpreflight.Decision{
		Allowed: true,
		Count: accountingapp.CountResult{
			InputTokens:  1_000_000,
			OutputTokens: 0,
		},
		AdjustedMaxOutputTokens: &adjusted,
	}

	got := attemptAuthoritySpendAmount(catalog, cand, decision)
	want := accounting.EstimateCost(accounting.CostInput{
		Backend: "backend-1",
		Model:   "model-1",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
	}, catalog)

	if got.Value != want.NanoUnits {
		t.Fatalf("spend=%d want %d (input+adjusted output)", got.Value, want.NanoUnits)
	}
	if got.Value == 1_000_000_000 {
		t.Fatal("spend must not remain input-only when AdjustedMaxOutputTokens is set")
	}
}

func TestAttemptAuthoritySpendAmount_usesCountOutputBound(t *testing.T) {
	t.Parallel()

	catalog := phase12SpendCatalog(t)
	decision := accountingpreflight.Decision{
		Allowed: true,
		Count: accountingapp.CountResult{
			InputTokens:  1_000_000,
			OutputTokens: 500_000,
		},
	}
	got := attemptAuthoritySpendAmount(catalog, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
	}, decision)
	want := accounting.EstimateCost(accounting.CostInput{
		Backend: "backend-1",
		Model:   "model-1",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 500_000},
	}, catalog)
	if got.Value != want.NanoUnits {
		t.Fatalf("spend=%d want %d", got.Value, want.NanoUnits)
	}
}

func TestAttemptAuthorityUsageAmount_infersTotalWithoutSubcomponentDoubleCount(t *testing.T) {
	t.Parallel()

	ev := lipapi.Event{
		Kind:             lipapi.EventUsageDelta,
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  40,
		CacheWriteTokens: 10,
		ReasoningTokens:  5,
		TotalTokens:      0,
	}
	got := attemptAuthorityUsageAmount(ev, authoritydomain.Amount{Unit: authoritydomain.AmountUnitTotalTokens})
	if got.Value != 120 {
		t.Fatalf("total=%d want 120 (input+output; cache/reasoning are subcomponents)", got.Value)
	}
}
