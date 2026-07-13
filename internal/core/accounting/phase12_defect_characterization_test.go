package accounting_test

import (
	"math"
	"math/big"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
)

// Phase 1.2 characterization (dual-plane-economics-and-concurrency-foundation):
// lock current monetary/token-component defects. Assertions describe TODAY's
// behavior; Phase 2 must invert them to the desired semantics noted in each test.

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

// TestEstimateCost_explicitZeroProviderCostCurrentlyFallsBackToEstimate locks F-07:
// ProviderCost.NanoUnits == 0 is treated like "absent" and falls through to catalog
// estimation. Phase 2 flip: authoritative zero must win with Source=provider_reported.
func TestEstimateCost_explicitZeroProviderCostCurrentlyFallsBackToEstimate(t *testing.T) {
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
		},
	}, catalog)

	// Desired (Phase 2): NanoUnits=0, Source=provider_reported.
	// Current: falls back to estimate of 1e9 nanos for 1M input tokens @ $1/1M.
	if got.Source != accounting.CostSourceEstimated {
		t.Fatalf("current defect characterization: Source=%q want estimated (zero provider cost falls back); Phase 2 must keep provider_reported", got.Source)
	}
	if got.NanoUnits != 1_000_000_000 {
		t.Fatalf("current defect characterization: NanoUnits=%d want 1000000000 catalog estimate; Phase 2 must keep authoritative zero", got.NanoUnits)
	}
}

// TestEstimateCost_explicitZeroCachedRateCurrentlyFallsBack locks F-07 rate presence:
// CachedInputPer1M "0" parses to 0 and fallbackPrice replaces it with InputPer1M.
// Phase 2 flip: explicit zero rate stays zero (no fallback).
func TestEstimateCost_explicitZeroCachedRateCurrentlyFallsBack(t *testing.T) {
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

	// Derive: non-cached input=0, cache-read=1M charged via fallback to InputPer1M → 1e9.
	// Desired (Phase 2): cache line at explicit zero → total 0 for this usage.
	if got.NanoUnits != 1_000_000_000 {
		t.Fatalf("current defect characterization: NanoUnits=%d want 1000000000 (zero cache rate fell back to input); Phase 2 must charge 0 for explicit zero cache rate", got.NanoUnits)
	}
}

// TestEstimateCost_explicitZeroReasoningRateCurrentlyFallsBack mirrors the cache-rate
// defect for reasoning output rates.
func TestEstimateCost_explicitZeroReasoningRateCurrentlyFallsBack(t *testing.T) {
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

	// non-reasoning output=0, reasoning=1M charged via fallback to OutputPer1M → 2e9.
	if got.NanoUnits != 2_000_000_000 {
		t.Fatalf("current defect characterization: NanoUnits=%d want 2000000000 (zero reasoning rate fell back); Phase 2 must charge 0", got.NanoUnits)
	}
}

// TestEstimateCost_overflowCurrentlyWrapsUnchecked locks unchecked int64 multiply in
// costForTokens. Phase 2 flip: checked arithmetic must error or saturate typed.
func TestEstimateCost_overflowCurrentlyWrapsUnchecked(t *testing.T) {
	t.Parallel()

	// Choose tokens and price so tokens*pricePer1M overflows int64 before /1e6.
	const tokens int64 = 10_000_000_000        // 10B
	const pricePer1M int64 = 1_000_000_000_000 // 1e12 nanos per 1M tokens ($1000/1M)
	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:     "openai",
		Model:       "gpt-test",
		InputPer1M:  "1000",
		OutputPer1M: "0",
	})

	got := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage:   accounting.TokenUsage{InputTokens: tokens},
	}, catalog)

	product := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(pricePer1M))
	if product.Cmp(big.NewInt(math.MaxInt64)) <= 0 {
		t.Fatalf("test inputs do not overflow int64 product: %s", product.String())
	}
	exact := new(big.Int).Div(new(big.Int).Set(product), big.NewInt(1_000_000))

	if got.Unavailable {
		t.Fatal("current defect characterization: overflow is silent wrap, not Unavailable; Phase 2 must surface checked arithmetic failure")
	}

	// Wrapped Go multiply then divide differs from exact big.Int quotient.
	if exact.IsInt64() && got.NanoUnits == exact.Int64() {
		t.Fatalf("expected silent int64 wrap to diverge from exact %s; got NanoUnits=%d (adjust inputs)", exact.String(), got.NanoUnits)
	}
	tok, price := tokens, pricePer1M
	wrapped := tok * price / 1_000_000 // intentional same unchecked path (non-const to allow wrap)
	if got.NanoUnits != wrapped {
		t.Fatalf("EstimateCost NanoUnits=%d want unchecked wrap value %d", got.NanoUnits, wrapped)
	}
}

// TestPhase12_desiredMonetarySemanticsCurrentlyAbsent records one-shot RED evidence:
// desired Phase 2 outcomes are not yet true. When Phase 2 lands, this test must fail
// and be deleted or inverted alongside the characterization tests above.
func TestPhase12_desiredMonetarySemanticsCurrentlyAbsent(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t, accounting.ModelPriceConfig{
		Backend:          "openai",
		Model:            "gpt-test",
		InputPer1M:       "1.00",
		CachedInputPer1M: "0",
		OutputPer1M:      "2.00",
	})

	zeroProvider := accounting.EstimateCost(accounting.CostInput{
		Backend:      "openai",
		Model:        "gpt-test",
		Usage:        accounting.TokenUsage{InputTokens: 1_000_000},
		ProviderCost: accounting.ProviderCost{NanoUnits: 0, Currency: "USD", Source: accounting.CostSourceProviderReported},
	}, catalog)
	if zeroProvider.Source == accounting.CostSourceProviderReported && zeroProvider.NanoUnits == 0 {
		t.Fatal("desired zero-provider-cost semantics already present; Phase 2 flip of characterization tests is due")
	}

	zeroCacheRate := accounting.EstimateCost(accounting.CostInput{
		Backend: "openai",
		Model:   "gpt-test",
		Usage:   accounting.TokenUsage{InputTokens: 1_000_000, CacheReadTokens: 1_000_000},
	}, catalog)
	if zeroCacheRate.NanoUnits == 0 {
		t.Fatal("desired explicit-zero cache rate semantics already present; Phase 2 flip due")
	}
}
