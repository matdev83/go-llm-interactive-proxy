package plane_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/plane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

func TestMoneyFromUsageEvent_ExplicitPresenceOnly(t *testing.T) {
	t.Parallel()
	if plane.MoneyFromUsageEvent(lipapi.Event{CostNanoUnits: 99, Currency: "USD", CostSource: "provider_reported"}) != nil {
		t.Fatal("nonzero cost without CostPresent must stay absent")
	}
	got := plane.MoneyFromUsageEvent(lipapi.Event{CostPresent: true, CostNanoUnits: 0, Currency: "EUR"})
	if got == nil || !got.Present || got.NanoUnits != 0 || got.Currency != "EUR" {
		t.Fatalf("authoritative zero lost: %+v", got)
	}
}

func TestQuantitiesFromUsageEvent_EmptyUnmarkedUsageDeltaOmits(t *testing.T) {
	t.Parallel()
	qs := plane.QuantitiesFromUsageEvent(lipapi.Event{Kind: lipapi.EventUsageDelta})
	if len(qs) != 0 {
		t.Fatalf("all-zero unmarked UsageDelta must omit quantities; got %+v", qs)
	}
}

func TestQuantitiesFromUsageEvent_LegacyUnmarkedNonzeroOnly(t *testing.T) {
	t.Parallel()
	qs := plane.QuantitiesFromUsageEvent(lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     3,
		OutputTokens:    0,
		CacheReadTokens: 0,
		ReasoningTokens: 0,
		TotalTokens:     0,
	})
	if len(qs) != 2 {
		t.Fatalf("want input + derived total only; got %+v", qs)
	}
	in, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentInputToken)
	if !ok || in != 3 {
		t.Fatalf("input=%d ok=%v", in, ok)
	}
	total, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentTotalToken)
	if !ok || total != 3 {
		t.Fatalf("derived total=%d ok=%v", total, ok)
	}
}

func TestQuantitiesFromUsageEvent_ExplicitZeroPresenceRetained(t *testing.T) {
	t.Parallel()
	qs := plane.QuantitiesFromUsageEvent(lipapi.Event{
		Kind: lipapi.EventUsageDelta,
		UsagePresence: lipapi.UsagePresence{
			InputTokens: true, OutputTokens: true, TotalTokens: true,
		},
		InputTokens: 0, OutputTokens: 0, TotalTokens: 0,
	})
	in, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentInputToken)
	if !ok || in != 0 {
		t.Fatalf("authoritative zero input lost: ok=%v v=%d", ok, in)
	}
}

func TestCustomerPlaneUsageEvent_StripsProviderScopesAndMoney(t *testing.T) {
	t.Parallel()
	ev := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		CostNanoUnits: 999,
		Currency:      "USD",
		CostPresent:   true,
		CostSource:    string(lipapi.UsageSourceProviderReported),
		UsageScopes: []lipapi.ScopedUsageDelta{
			{
				InputTokens: 40, OutputTokens: 12, TotalTokens: 52,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated,
				},
			},
			{
				InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
				},
			},
		},
	}
	got := plane.CustomerPlaneUsageEvent(ev)
	if got.InputTokens != 40 || got.OutputTokens != 12 {
		t.Fatalf("customer plane tokens in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.CostPresent || got.CostNanoUnits != 0 || got.Currency != "" {
		t.Fatalf("customer plane must strip money; got %+v", got)
	}
}
