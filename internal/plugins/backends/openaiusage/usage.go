package openaiusage

import (
	"encoding/json"
	"math/big"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"
)

const (
	lipCacheWriteTokensKey  = "x_lip_cache_write_tokens"
	usageCostKey            = "cost"
	defaultProviderCurrency = "USD"
	providerCostNanoScale   = int64(1_000_000_000)
)

func ChatUsageEvent(usage openai.CompletionUsage) lipapi.Event {
	ev := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     safecast.IntFromInt64Clamp(usage.PromptTokens),
		OutputTokens:    safecast.IntFromInt64Clamp(usage.CompletionTokens),
		CacheReadTokens: safecast.IntFromInt64Clamp(usage.PromptTokensDetails.CachedTokens),
		ReasoningTokens: safecast.IntFromInt64Clamp(usage.CompletionTokensDetails.ReasoningTokens),
		TotalTokens:     safecast.IntFromInt64Clamp(usage.TotalTokens),
		RawUsageJSON:    rawJSON(usage.RawJSON(), usage),
	}
	applyPromptDetailsExtensions(&ev, usage.PromptTokensDetails.JSON.ExtraFields, usage.PromptTokensDetails.RawJSON())
	applyUsageCostExtensions(&ev, usage.JSON.ExtraFields, usage.RawJSON())
	return ev
}

func ResponsesUsageEvent(u responses.ResponseUsage) lipapi.Event {
	ev := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     safecast.IntFromInt64Clamp(u.InputTokens),
		OutputTokens:    safecast.IntFromInt64Clamp(u.OutputTokens),
		CacheReadTokens: safecast.IntFromInt64Clamp(u.InputTokensDetails.CachedTokens),
		ReasoningTokens: safecast.IntFromInt64Clamp(u.OutputTokensDetails.ReasoningTokens),
		TotalTokens:     safecast.IntFromInt64Clamp(u.TotalTokens),
		RawUsageJSON:    rawJSON(u.RawJSON(), u),
	}
	applyPromptDetailsExtensions(&ev, u.InputTokensDetails.JSON.ExtraFields, u.InputTokensDetails.RawJSON())
	applyUsageCostExtensions(&ev, u.JSON.ExtraFields, u.RawJSON())
	return ev
}

func rawJSON(raw string, usage any) string {
	if raw != "" {
		return raw
	}
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}

func applyPromptDetailsExtensions(ev *lipapi.Event, extras map[string]respjson.Field, detailsRaw string) {
	if ev == nil {
		return
	}
	if len(extras) > 0 {
		if f, ok := extras[lipCacheWriteTokensKey]; ok && f.Valid() {
			ev.CacheWriteTokens = intFieldFromJSON(f.Raw())
		}
	}
	if ev.CacheWriteTokens == 0 {
		ev.CacheWriteTokens = cacheWriteFromDetailsJSON(detailsRaw)
	}
}

func cacheWriteFromDetailsJSON(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var probe struct {
		CacheWrite int `json:"x_lip_cache_write_tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return 0
	}
	return probe.CacheWrite
}

func applyUsageCostExtensions(ev *lipapi.Event, extras map[string]respjson.Field, usageRaw string) {
	if ev == nil {
		return
	}
	if len(extras) > 0 {
		if f, ok := extras[usageCostKey]; ok && f.Valid() {
			applyProviderCost(ev, f.Raw())
		}
	}
	if ev.CostNanoUnits == 0 {
		applyProviderCost(ev, providerCostRawFromUsageJSON(usageRaw))
	}
}

func providerCostRawFromUsageJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var probe struct {
		Cost json.RawMessage `json:"cost"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || len(probe.Cost) == 0 {
		return ""
	}
	return string(probe.Cost)
}

func applyProviderCost(ev *lipapi.Event, raw string) {
	nano, ok := providerCostNanoUnits(raw)
	if !ok || nano <= 0 {
		return
	}
	ev.CostNanoUnits = nano
	ev.Currency = defaultProviderCurrency
	ev.CostSource = accounting.CostSourceProviderReported
}

func intFieldFromJSON(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var n int64
	if err := json.Unmarshal([]byte(raw), &n); err == nil {
		return safecast.IntFromInt64Clamp(n)
	}
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err == nil {
		return safecast.IntFromInt64Clamp(int64(f))
	}
	return 0
}

func providerCostNanoUnits(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	rat, ok := new(big.Rat).SetString(raw)
	if !ok {
		var f float64
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return 0, false
		}
		if f <= 0 {
			return 0, false
		}
		return int64(f*float64(providerCostNanoScale) + 0.5), true
	}
	if rat.Sign() <= 0 {
		return 0, false
	}
	rat.Mul(rat, big.NewRat(providerCostNanoScale, 1))
	f, _ := rat.Float64()
	if f <= 0 {
		return 0, false
	}
	return int64(f + 0.5), true
}
