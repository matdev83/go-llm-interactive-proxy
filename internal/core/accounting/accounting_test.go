package accounting_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
)

func TestDeriveTokenBreakdown_clampsDerivedCategories(t *testing.T) {
	t.Parallel()

	got := accounting.DeriveTokenBreakdown(accounting.TokenUsage{
		InputTokens:      10,
		CacheReadTokens:  3,
		CacheWriteTokens: 2,
		OutputTokens:     7,
		ReasoningTokens:  4,
	})

	if got.NonCachedInputTokens != 5 {
		t.Fatalf("NonCachedInputTokens: got %d want 5", got.NonCachedInputTokens)
	}
	if got.NonReasoningOutputTokens != 3 {
		t.Fatalf("NonReasoningOutputTokens: got %d want 3", got.NonReasoningOutputTokens)
	}
}

func TestEstimateCost_providerReportedCostWins(t *testing.T) {
	t.Parallel()

	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "test-v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:          "openai",
			Model:            "gpt-test",
			InputPer1M:       "1.00",
			OutputPer1M:      "2.00",
			CachedInputPer1M: "0.10",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage:   accounting.TokenUsage{InputTokens: 1000, OutputTokens: 1000},
		ProviderCost: accounting.ProviderCost{
			NanoUnits: 123,
			Currency:  "EUR",
			Source:    "provider_reported",
		},
	}, catalog)

	if got.NanoUnits != 123 || got.Currency != "EUR" || got.Source != accounting.CostSourceProviderReported {
		t.Fatalf("provider cost not preserved: %+v", got)
	}
	if got.CatalogVersion != "" {
		t.Fatalf("provider cost should not attach catalog version: %+v", got)
	}
}

func TestEstimateCost_usesTokenCategoryPrices(t *testing.T) {
	t.Parallel()

	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "test-v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:              "openai",
			Model:                "gpt-test",
			InputPer1M:           "1.00",
			CachedInputPer1M:     "0.25",
			CacheWriteInputPer1M: "1.50",
			OutputPer1M:          "2.00",
			ReasoningOutputPer1M: "3.00",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage: accounting.TokenUsage{
			InputTokens:      1_000_000,
			CacheReadTokens:  200_000,
			CacheWriteTokens: 100_000,
			OutputTokens:     500_000,
			ReasoningTokens:  100_000,
		},
	}, catalog)

	const want = int64(2_000_000_000)
	if got.NanoUnits != want {
		t.Fatalf("NanoUnits: got %d want %d (%+v)", got.NanoUnits, want, got)
	}
	if got.Currency != "USD" || got.Source != accounting.CostSourceEstimated || got.CatalogVersion != "test-v1" {
		t.Fatalf("estimate metadata: %+v", got)
	}
}

func TestEstimateCost_missingPriceIsUnavailable(t *testing.T) {
	t.Parallel()

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "missing",
		Model:   "model",
		Usage:   accounting.TokenUsage{InputTokens: 1},
	}, accounting.PriceCatalog{})

	if got.Source != accounting.CostSourceUnavailable || !got.Unavailable {
		t.Fatalf("want unavailable cost, got %+v", got)
	}
}
