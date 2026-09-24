package service

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type anthropicCacheCreationFields struct {
	Ephemeral5mInputTokens *int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens"`
}

type anthropicServerToolUseFields struct {
	WebFetchRequests  *int `json:"web_fetch_requests"`
	WebSearchRequests *int `json:"web_search_requests"`
}

func minimaxAnthropicUsageEvent(u anthropicUsageFields, _ bool, requestID string) *lipapi.Event {
	input, inputPresent := minimaxIntValue(u.InputTokens)
	output, outputPresent := minimaxIntValue(u.OutputTokens)
	cacheRead, cacheReadPresent := minimaxIntValue(u.CacheReadInputTokens)
	cacheWrite, cacheWritePresent := minimaxIntValue(u.CacheCreationInputTokens)
	reasoning, reasoningPresent := minimaxIntValue(u.ReasoningTokens)
	if !reasoningPresent {
		reasoning, reasoningPresent = minimaxIntValue(u.ThinkingTokens)
	}
	total, totalPresent := minimaxIntValue(u.TotalTokens)
	presence := lipapi.UsagePresence{
		InputTokens: inputPresent, OutputTokens: outputPresent,
		CacheReadTokens: cacheReadPresent, CacheWriteTokens: cacheWritePresent,
		ReasoningTokens: reasoningPresent, TotalTokens: totalPresent,
	}
	if !presence.Any() {
		return nil
	}
	return &lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: input, OutputTokens: output,
		CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
		ReasoningTokens: reasoning, TotalTokens: total, UsagePresence: presence,
		RawUsageJSON: marshalProviderUsage(u),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:             lipapi.UsagePlaneProviderBillable,
			Source:            lipapi.UsageSourceProviderReported,
			Authority:         lipapi.UsageAuthorityAuthoritative,
			ProviderRequestID: strings.TrimSpace(requestID),
			DedupeKey:         "minimexoauth.anthropic:" + strings.TrimSpace(requestID),
			ServiceContext:    strings.TrimSpace(u.ServiceTier),
		},
	}
}

// mergeAnthropicUsageEvent joins partial message_start/message_delta snapshots
// before the V1 sideband is emitted. MiniMax's Anthropic-compatible stream can
// put input/cache on message_start and output on a later delta.
func mergeAnthropicUsageEvent(previous, next lipapi.Event) lipapi.Event {
	out := previous
	out.Kind = next.Kind
	copyCounter := func(present bool, value int, dst *int, dstPresent *bool) {
		if !present {
			return
		}
		*dst = value
		*dstPresent = true
	}
	copyCounter(next.UsagePresence.InputTokens, next.InputTokens, &out.InputTokens, &out.UsagePresence.InputTokens)
	copyCounter(next.UsagePresence.OutputTokens, next.OutputTokens, &out.OutputTokens, &out.UsagePresence.OutputTokens)
	copyCounter(next.UsagePresence.CacheReadTokens, next.CacheReadTokens, &out.CacheReadTokens, &out.UsagePresence.CacheReadTokens)
	copyCounter(next.UsagePresence.CacheWriteTokens, next.CacheWriteTokens, &out.CacheWriteTokens, &out.UsagePresence.CacheWriteTokens)
	copyCounter(next.UsagePresence.ReasoningTokens, next.ReasoningTokens, &out.ReasoningTokens, &out.UsagePresence.ReasoningTokens)
	copyCounter(next.UsagePresence.TotalTokens, next.TotalTokens, &out.TotalTokens, &out.UsagePresence.TotalTokens)
	if strings.TrimSpace(next.Accounting.ProviderRequestID) != "" {
		out.Accounting.ProviderRequestID = strings.TrimSpace(next.Accounting.ProviderRequestID)
	}
	if strings.TrimSpace(next.Accounting.ProviderChargeID) != "" {
		out.Accounting.ProviderChargeID = strings.TrimSpace(next.Accounting.ProviderChargeID)
	}
	if strings.TrimSpace(next.Accounting.ProviderAccountKey) != "" {
		out.Accounting.ProviderAccountKey = strings.TrimSpace(next.Accounting.ProviderAccountKey)
	}
	if strings.TrimSpace(next.Accounting.ServiceContext) != "" {
		out.Accounting.ServiceContext = strings.TrimSpace(next.Accounting.ServiceContext)
	}
	if strings.TrimSpace(next.Accounting.DedupeKey) != "" {
		out.Accounting.DedupeKey = strings.TrimSpace(next.Accounting.DedupeKey)
	}
	if strings.TrimSpace(next.RawUsageJSON) != "" {
		out.RawUsageJSON = next.RawUsageJSON
	}
	return out
}

func marshalProviderUsage(usage any) string {
	raw, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(raw)
}

func minimaxAnthropicNativeMeasures(raw string) []sdkmetering.Measure {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var usage anthropicUsageFields
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return nil
	}
	measures := make([]sdkmetering.Measure, 0, 4)
	appendCache := func(name string, value *int) {
		if value == nil || *value < 0 {
			return
		}
		decimal := sdkmetering.Decimal{Coefficient: strconv.Itoa(*value)}
		measures = append(measures, sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentCacheWriteInputToken, Unit: sdkmetering.UnitToken, SchemaID: "anthropic.usage.v2", Dimensions: []sdkmetering.Dimension{{Name: "cache_lifetime", Value: name}}},
			Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: "anthropic.usage.cache_lifetime.v1",
		})
	}
	if usage.CacheCreation != nil {
		appendCache("5m", usage.CacheCreation.Ephemeral5mInputTokens)
		appendCache("1h", usage.CacheCreation.Ephemeral1hInputTokens)
	}
	appendTool := func(name string, value *int) {
		if value == nil || *value < 0 {
			return
		}
		decimal := sdkmetering.Decimal{Coefficient: strconv.Itoa(*value)}
		measures = append(measures, sdkmetering.Measure{
			Key:   sdkmetering.ComponentKey{Direction: sdkmetering.DirectionNone, Component: sdkmetering.ComponentToolQuery, Unit: sdkmetering.UnitCount, SchemaID: "anthropic.usage.v2", Dimensions: []sdkmetering.Dimension{{Name: "tool", Value: name}}},
			Value: &decimal, Quality: sdkmetering.QualityObserved, MethodRef: "anthropic.usage.server_tool.v1",
		})
	}
	if usage.ServerToolUse != nil {
		appendTool("web_fetch", usage.ServerToolUse.WebFetchRequests)
		appendTool("web_search", usage.ServerToolUse.WebSearchRequests)
	}
	return measures
}

func minimaxIntValue(value *int) (int, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}
	return *value, true
}
