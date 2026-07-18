package accounting_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
)

// Phase 2.5: required base rates absent rejected; optional absent allowed;
// decimal overflow / excess precision rejected via shared public helpers.

func TestPhase25_NewPriceCatalog_RejectsAbsentRequiredBaseRates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		row  accounting.ModelPriceConfig
	}{
		{
			name: "missing_input",
			row: accounting.ModelPriceConfig{
				Backend: "b", Model: "m", OutputPer1M: "1.00",
			},
		},
		{
			name: "missing_output",
			row: accounting.ModelPriceConfig{
				Backend: "b", Model: "m", InputPer1M: "1.00",
			},
		},
		{
			name: "whitespace_input",
			row: accounting.ModelPriceConfig{
				Backend: "b", Model: "m", InputPer1M: "  ", OutputPer1M: "1.00",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
				Version: "v1", Currency: "USD", Models: []accounting.ModelPriceConfig{tc.row},
			})
			if err == nil {
				t.Fatal("absent required base rate must fail catalog build (req 4.10)")
			}
		})
	}
}

func TestPhase25_NewPriceCatalog_OptionalAbsentAndExplicitZero(t *testing.T) {
	t.Parallel()
	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:     "b",
			Model:       "m",
			InputPer1M:  "0",
			OutputPer1M: "2.00",
			// CachedInputPer1M absent → fallback to input (still 0)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "b", Model: "m",
		Usage: accounting.TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000},
	}, catalog)
	if got.Unavailable || got.NanoUnits != 0 {
		t.Fatalf("authoritative zero input + absent cache optional: %+v", got)
	}
}

func TestPhase25_NewPriceCatalog_RejectsOverflowDecimal(t *testing.T) {
	t.Parallel()
	_, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:     "b",
			Model:       "m",
			InputPer1M:  "999999999999999999999999999999999",
			OutputPer1M: "1",
		}},
	})
	if err == nil {
		t.Fatal("overflow decimal must fail catalog build (req 4.10; no silent Int64 truncation)")
	}
}

func TestPhase25_NewPriceCatalog_RejectsExcessPrecision(t *testing.T) {
	t.Parallel()
	_, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:     "b",
			Model:       "m",
			InputPer1M:  "1.1234567891",
			OutputPer1M: "1",
		}},
	})
	if err == nil {
		t.Fatal("excess precision must fail catalog build (req 4.10)")
	}
}
