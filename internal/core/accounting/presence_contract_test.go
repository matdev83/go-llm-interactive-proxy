package accounting_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
)

// Phase 1.1 companion (green): authoritative zero versus absent provider cost must stay distinct (req 2.9).

func TestEstimateCost_AuthoritativeZeroDistinctFromAbsent(t *testing.T) {
	t.Parallel()

	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "presence-v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:     "openai",
			Model:       "gpt-test",
			InputPer1M:  "1.00",
			OutputPer1M: "2.00",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("absent_provider_cost_falls_back_to_estimate", func(t *testing.T) {
		t.Parallel()
		got := accounting.EstimateCost(accounting.CostInput{
			Backend: "openai",
			Model:   "gpt-test",
			Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 0},
			ProviderCost: accounting.ProviderCost{
				NanoUnits: 0,
				Currency:  "USD",
				Source:    accounting.CostSourceProviderReported,
				Present:   false,
			},
		}, catalog)
		if got.Unavailable || got.Source != accounting.CostSourceEstimated {
			t.Fatalf("absent provider cost must estimate; got %+v", got)
		}
		if got.NanoUnits != 1_000_000_000 {
			t.Fatalf("estimated NanoUnits=%d", got.NanoUnits)
		}
	})

	t.Run("present_zero_provider_cost_is_authoritative", func(t *testing.T) {
		t.Parallel()
		got := accounting.EstimateCost(accounting.CostInput{
			Backend: "openai",
			Model:   "gpt-test",
			Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 0},
			ProviderCost: accounting.ProviderCost{
				NanoUnits: 0,
				Currency:  "EUR",
				Source:    accounting.CostSourceProviderReported,
				Present:   true,
			},
		}, catalog)
		if got.Unavailable || got.Source != accounting.CostSourceProviderReported {
			t.Fatalf("present zero must stay provider_reported; got %+v", got)
		}
		if got.NanoUnits != 0 || got.Currency != "EUR" {
			t.Fatalf("authoritative zero not preserved: %+v", got)
		}
		if got.CatalogVersion != "" {
			t.Fatalf("provider zero must not attach catalog version: %+v", got)
		}
	})
}
