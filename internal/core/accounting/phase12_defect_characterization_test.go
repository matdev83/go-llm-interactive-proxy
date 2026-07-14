package accounting_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
)

// Phase 2.2: authoritative zero costs, explicit-zero rates, and checked overflow.

func testCatalog(t *testing.T, models ...accounting.ModelPriceConfig) accounting.PriceCatalog {
	t.Helper()
	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "phase12-v1",
		Currency: "USD",
		Models:   models,
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestEstimateCost_authoritativeZeroProviderCostNotEstimated(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:     "openai",
		Model:       "gpt-test",
		InputPer1M:  "1.00",
		OutputPer1M: "2.00",
	})

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000, OutputTokens: 0},
		ProviderCost: accounting.ProviderCost{
			NanoUnits: 0,
			Currency:  "USD",
			Source:    accounting.CostSourceProviderReported,
			Present:   true,
		},
	}, catalog)

	if got.Source != accounting.CostSourceProviderReported {
		t.Fatalf("Source=%q want provider_reported", got.Source)
	}
	if got.NanoUnits != 0 {
		t.Fatalf("NanoUnits=%d want 0 (authoritative zero)", got.NanoUnits)
	}
	if got.Unavailable {
		t.Fatal("authoritative zero must not be Unavailable")
	}
}

func TestEstimateCost_explicitZeroCachedRateDoesNotFallback(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:          "openai",
		Model:            "gpt-test",
		InputPer1M:       "1.00",
		CachedInputPer1M: "0",
		OutputPer1M:      "2.00",
	})

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage: accounting.TokenUsage{
			InputTokens:     1_000_000,
			CacheReadTokens: 1_000_000,
		},
	}, catalog)

	if got.NanoUnits != 0 {
		t.Fatalf("NanoUnits=%d want 0 (explicit zero cache rate; non-cached input is 0)", got.NanoUnits)
	}
}

func TestEstimateCost_explicitZeroReasoningRateDoesNotFallback(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:              "openai",
		Model:                "gpt-test",
		InputPer1M:           "1.00",
		OutputPer1M:          "2.00",
		ReasoningOutputPer1M: "0",
	})

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage: accounting.TokenUsage{
			OutputTokens:    1_000_000,
			ReasoningTokens: 1_000_000,
		},
	}, catalog)

	if got.NanoUnits != 0 {
		t.Fatalf("NanoUnits=%d want 0 (explicit zero reasoning rate; non-reasoning output is 0)", got.NanoUnits)
	}
}

func TestEstimateCost_overflowReturnsUnavailable(t *testing.T) {
	t.Parallel()

	// MaxInt64 tokens at 2e6 nanos/1M → quotient MaxInt64*2 overflows int64.
	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:     "openai",
		Model:       "gpt-test",
		InputPer1M:  "0.002",
		OutputPer1M: "0",
	})

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage:   accounting.TokenUsage{InputTokens: 1<<63 - 1}, // math.MaxInt64
	}, catalog)

	if !got.Unavailable || got.Source != accounting.CostSourceUnavailable {
		t.Fatalf("want Unavailable overflow result, got %+v", got)
	}
}

func TestEstimateCost_absentProviderCostStillEstimates(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:     "openai",
		Model:       "gpt-test",
		InputPer1M:  "1.00",
		OutputPer1M: "2.00",
	})

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000},
		// Present=false → catalog estimate
	}, catalog)

	if got.Source != accounting.CostSourceEstimated || got.NanoUnits != 1_000_000_000 {
		t.Fatalf("absent provider cost should estimate, got %+v", got)
	}
}

func TestSubMoneyChecked(t *testing.T) {
	t.Parallel()

	got, ok := accounting.SubMoneyChecked(10, 3)
	if !ok || got != 7 {
		t.Fatalf("SubMoneyChecked(10,3)=%d,%v want 7,true", got, ok)
	}
	if _, ok := accounting.SubMoneyChecked(3, 10); ok {
		t.Fatal("expected underflow failure")
	}
}
