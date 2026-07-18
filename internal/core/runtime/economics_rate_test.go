package runtime

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestAttemptAuthoritySpendAmountFromQuantities_IncludesCacheAndReasoning(t *testing.T) {
	t.Parallel()
	catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
		Version:  "v1",
		Currency: "USD",
		Models: []accounting.ModelPriceConfig{{
			Backend:              "be",
			Model:                "m",
			InputPer1M:           "1",
			CachedInputPer1M:     "0.1",
			CacheWriteInputPer1M: "1.25",
			OutputPer1M:          "2",
			ReasoningOutputPer1M: "3",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "be", Model: "m"}}
	withExtras := attemptAuthoritySpendAmountFromQuantities(catalog, cand, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1_000_000, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 1_000_000, Present: true},
		{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: 1_000_000, Present: true},
		{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: 0, Present: true},
		{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: 1_000_000, Present: true},
	})
	inputOutputOnly := attemptAuthoritySpendAmountFromQuantities(catalog, cand, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1_000_000, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 1_000_000, Present: true},
	})
	if withExtras.Value <= inputOutputOnly.Value {
		t.Fatalf("cache/reasoning must increase spend: with=%d without=%d", withExtras.Value, inputOutputOnly.Value)
	}
}
