package openaiusage

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/safecast"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
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
	input, inputPresent := nonNegativeProviderCount(usage.PromptTokens, usage.JSON.PromptTokens.Valid())
	output, outputPresent := nonNegativeProviderCount(usage.CompletionTokens, usage.JSON.CompletionTokens.Valid())
	cached, cachedPresent := nonNegativeProviderCount(usage.PromptTokensDetails.CachedTokens, usage.PromptTokensDetails.JSON.CachedTokens.Valid())
	reasoning, reasoningPresent := nonNegativeProviderCount(usage.CompletionTokensDetails.ReasoningTokens, usage.CompletionTokensDetails.JSON.ReasoningTokens.Valid())
	total, totalPresent := nonNegativeProviderCount(usage.TotalTokens, usage.JSON.TotalTokens.Valid())
	ev := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     input,
		OutputTokens:    output,
		CacheReadTokens: cached,
		ReasoningTokens: reasoning,
		TotalTokens:     total,
		UsagePresence: lipapi.UsagePresence{
			InputTokens: inputPresent, OutputTokens: outputPresent,
			CacheReadTokens: cachedPresent, ReasoningTokens: reasoningPresent,
			TotalTokens: totalPresent,
		},
		RawUsageJSON: rawJSON(usage.RawJSON(), usage),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "openai.chat.usage:stream",
		},
	}
	applyPromptDetailsExtensions(&ev, usage.PromptTokensDetails.JSON.ExtraFields, usage.PromptTokensDetails.RawJSON())
	applyUsageCostExtensions(&ev, usage.JSON.ExtraFields, usage.RawJSON())
	return ev
}

func ResponsesUsageEvent(u responses.ResponseUsage) lipapi.Event {
	input, inputPresent := nonNegativeProviderCount(u.InputTokens, u.JSON.InputTokens.Valid())
	output, outputPresent := nonNegativeProviderCount(u.OutputTokens, u.JSON.OutputTokens.Valid())
	cached, cachedPresent := nonNegativeProviderCount(u.InputTokensDetails.CachedTokens, u.InputTokensDetails.JSON.CachedTokens.Valid())
	reasoning, reasoningPresent := nonNegativeProviderCount(u.OutputTokensDetails.ReasoningTokens, u.OutputTokensDetails.JSON.ReasoningTokens.Valid())
	total, totalPresent := nonNegativeProviderCount(u.TotalTokens, u.JSON.TotalTokens.Valid())
	ev := lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     input,
		OutputTokens:    output,
		CacheReadTokens: cached,
		ReasoningTokens: reasoning,
		TotalTokens:     total,
		UsagePresence: lipapi.UsagePresence{
			InputTokens: inputPresent, OutputTokens: outputPresent,
			CacheReadTokens: cachedPresent, ReasoningTokens: reasoningPresent,
			TotalTokens: totalPresent,
		},
		RawUsageJSON: rawJSON(u.RawJSON(), u),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative, DedupeKey: "openai.responses.usage:stream",
		},
	}
	applyPromptDetailsExtensions(&ev, u.InputTokensDetails.JSON.ExtraFields, u.InputTokensDetails.RawJSON())
	applyUsageCostExtensions(&ev, u.JSON.ExtraFields, u.RawJSON())
	return ev
}

func nonNegativeProviderCount(value int64, present bool) (int, bool) {
	if !present || value < 0 {
		return 0, false
	}
	converted := safecast.IntFromInt64Clamp(value)
	if int64(converted) != value {
		return 0, false
	}
	return converted, true
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
			if value, valid := parseIntFieldFromJSON(f.Raw()); valid {
				ev.CacheWriteTokens = value
				ev.UsagePresence.CacheWriteTokens = true
			}
		}
	}
	if ev.CacheWriteTokens == 0 {
		if value, ok := cacheWriteFromDetailsJSON(detailsRaw); ok {
			ev.CacheWriteTokens = value
			ev.UsagePresence.CacheWriteTokens = true
		}
	}
}

func cacheWriteFromDetailsJSON(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return 0, false
	}
	value, ok := probe[lipCacheWriteTokensKey]
	if !ok {
		return 0, false
	}
	return parseIntFieldFromJSON(string(value))
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
	if !ev.CostPresent {
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
	if !ok || nano < 0 {
		return
	}
	ev.CostNanoUnits = nano
	ev.Currency = defaultProviderCurrency
	ev.CostSource = accounting.CostSourceProviderReported
	ev.CostPresent = true
}

func intFieldFromJSON(raw string) int {
	value, _ := parseIntFieldFromJSON(raw)
	return value
}

func parseIntFieldFromJSON(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	decimal, err := sdkmetering.ParseDecimal(raw)
	if err != nil || decimal.Scale != 0 || strings.HasPrefix(raw, "-") || strings.HasPrefix(decimal.Coefficient, "-") {
		return 0, false
	}
	n, err := strconv.ParseInt(decimal.Coefficient, 10, 0)
	if err != nil || n < 0 {
		return 0, false
	}
	return int(n), true
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
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		scaled := f * float64(providerCostNanoScale)
		if math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(scaled + 0.5), true
	}
	if rat.Sign() < 0 {
		return 0, false
	}
	if rat.Sign() == 0 {
		return 0, true
	}
	rat.Mul(rat, big.NewRat(providerCostNanoScale, 1))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(rat.Num(), rat.Denom(), r)
	if new(big.Int).Mul(r, big.NewInt(2)).Cmp(rat.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}
