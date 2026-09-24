package openaiusage_test

// R7-C1D (PR #659 adversarial repair): bounded cached/reasoning token overlap
// slice.
//
// Primary provider-family containment (verified against the official OpenAI
// usage type definitions and prompt-caching guide):
//
//   - Chat: $.usage.prompt_tokens_details.cached_tokens is a subset of
//     $.usage.prompt_tokens ("Cached tokens present in the prompt"), and
//     $.usage.completion_tokens_details.reasoning_tokens is a subset of
//     $.usage.completion_tokens (the SDK documents that reasoning tokens are
//     "still counted in the total completion tokens for purposes of billing,
//     output, and context window limits").
//   - Responses: $.usage.input_tokens_details.cached_tokens is a subset of
//     $.usage.input_tokens, and
//     $.usage.output_tokens_details.reasoning_tokens is a subset of
//     $.usage.output_tokens. The official prompt-caching guide derives ordinary
//     input as input_tokens - cached_tokens - cache_write_tokens, and the cost
//     example subtracts cached_tokens from input_tokens.
//
// The adapter maps these quantities onto the host-neutral
// input/cache_read_input_token and output/reasoning_output_token components
// under metering.DefaultInclusionSchemaID (providerTokenMeasures), so the two
// containment edges must be declared in that same schema identity. They are
// subset edges, not partition members: cached and reasoning tokens are not the
// complete text/audio partition, and a child-only rule over one of them must
// never prove the parent aggregate's complete coverage.
//
// Every vector drives the real Chat/Responses adapter -> provider evidence
// buffer -> frozen tariff schema -> retail selection/rating path, never a
// hand-built measure slice.

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// wireCacheReasoningChatSuccess is the true Chat shape with only the cached and
// reasoning detail members present: the ordinary prompt/completion totals carry
// their included cached/reasoning subsets with no text/audio partition members.
const wireCacheReasoningChatSuccess = `{"id":"chat-cache","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}`

// wireCacheReasoningResponsesSuccess is the true Responses shape with only the
// cached and reasoning detail members present.
const wireCacheReasoningResponsesSuccess = `{"id":"resp-cache","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":3}}}`

// wireChatCacheReasoningUsage builds the same Chat shape with caller-supplied
// prompt/completion detail objects so absent, null and explicit-zero members can
// be exercised.
func wireChatCacheReasoningUsage(promptDetails, completionDetails string) string {
	return `{"id":"chat-cache","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":` +
		promptDetails + `,"completion_tokens_details":` + completionDetails + `}}`
}

// wireCachedReasoningRules prices the aggregate input/output totals and their
// included cached/reasoning children with deliberately distinct rates so an
// additive double bill (22 + 27 + 20 + 21 = 90) cannot coincide with any single
// aggregate amount.
func wireCachedReasoningRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-aggregate", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2"),
		wireLinearRule("output-aggregate", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "3"),
		wireLinearRule("input-cache-read", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken), "5"),
		wireLinearRule("output-reasoning", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken), "7"),
	}
}

// wireAggregateOnlyCachedReasoningRules prices only the inclusive aggregate
// parents; the cached/reasoning subsets have no rule of their own.
func wireAggregateOnlyCachedReasoningRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-aggregate", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentInputToken), "2"),
		wireLinearRule("output-aggregate", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken), "3"),
	}
}

// wireChildOnlyCachedReasoningRules prices only the cached/reasoning subsets and
// deliberately omits the inclusive aggregate summaries.
func wireChildOnlyCachedReasoningRules() []economics.RatingRule {
	return []economics.RatingRule{
		wireLinearRule("input-cache-read", wireDefaultKey(sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken), "5"),
		wireLinearRule("output-reasoning", wireDefaultKey(sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken), "7"),
	}
}

// TestOpenAIUsageEvidence_CachedReasoningBothPricedFailsClosed is the primary
// R7-C1D RED vector. The real adapter surfaces the aggregate input/output totals
// and their included cached/reasoning subsets; a schema-free tariff additively
// double bills them (11*2 + 9*3 + 4*5 + 3*7 = 90/0), while the frozen
// provider-family schema must classify the same both-priced tariff as a typed
// overlap with no payable quantity lines.
func TestOpenAIUsageEvidence_CachedReasoningBothPricedFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		path      string
		chat      string
		responses string
	}{
		{name: "chat", path: "chat", chat: wireCacheReasoningChatSuccess, responses: wireResponsesSuccess},
		{name: "responses", path: "responses", chat: wireVisionChatSuccess, responses: wireCacheReasoningResponsesSuccess},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chat, tc.responses)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, tc.path, wireIdentityForCall(callID, "cache-"+tc.name))[0]
			call, leg, policy := wireRetailCall(t, callID, observation)

			// Control: without the frozen schema the identical evidence and
			// tariff rate additively at 90/0.
			legacyResult, legacyErr := rateWireRetail(t, call, leg, policy, wireTariff(t, wireCachedReasoningRules()))
			if legacyErr != nil {
				t.Fatalf("schema-free control must rate additively: %v", legacyErr)
			}
			if total := wireValuationTotal(legacyResult.InferenceValuation); total != "90/0" {
				t.Fatalf("schema-free control total=%s, want additive 90 (11*2 + 9*3 + 4*5 + 3*7)", total)
			}
			for _, want := range []struct {
				direction sdkmetering.FlowDirection
				component string
				amount    string
			}{
				{sdkmetering.DirectionInput, sdkmetering.ComponentInputToken, "22/0"},
				{sdkmetering.DirectionOutput, sdkmetering.ComponentOutputToken, "27/0"},
				{sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken, "20/0"},
				{sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken, "21/0"},
			} {
				amount := wireComponentAmount(t, legacyResult.InferenceValuation, want.direction, want.component)
				if amount == nil || amount.CanonicalString() != want.amount {
					t.Fatalf("schema-free %s/%s amount=%v, want additive %s", want.direction, want.component, amount, want.amount)
				}
			}

			// Frozen schema: the same both-priced tariff is a typed overlap.
			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCachedReasoningRules()))
			if !wireOverlapRejected(err) {
				t.Fatalf("%s both-priced cached/reasoning err=%v, want typed overlap; total=%s",
					tc.name, err, wireValuationTotal(result.InferenceValuation))
			}
			if result.InferenceValuation.Completeness != economics.CompletenessConflict {
				t.Fatalf("%s both-priced completeness=%q, want conflict", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total == "90/0" {
				t.Fatalf("%s both-priced tariff must not post the additive 90 total", tc.name)
			}
		})
	}
}

// TestOpenAIUsageEvidence_CachedReasoningAggregateOnlyStaysComplete proves the
// subset edge is a partial containment: an aggregate-only tariff whose inclusive
// parents are priced and whose cached/reasoning subsets have no rule stays
// complete at the literal aggregate total 49/0 and never lets an included subset
// become independently billable.
func TestOpenAIUsageEvidence_CachedReasoningAggregateOnlyStaysComplete(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		path      string
		chat      string
		responses string
	}{
		{name: "chat", path: "chat", chat: wireCacheReasoningChatSuccess, responses: wireResponsesSuccess},
		{name: "responses", path: "responses", chat: wireVisionChatSuccess, responses: wireCacheReasoningResponsesSuccess},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, tc.chat, tc.responses)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, tc.path, wireIdentityForCall(callID, "agg-only-"+tc.name))[0]
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireAggregateOnlyCachedReasoningRules()))
			if err != nil {
				t.Fatalf("%s aggregate-only tariff must rate complete, got %v; lines=%+v", tc.name, err, result.InferenceValuation.Lines)
			}
			if result.InferenceValuation.Completeness != economics.CompletenessComplete {
				t.Fatalf("%s aggregate-only completeness=%q, want complete", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total != "49/0" {
				t.Fatalf("%s aggregate-only total=%s, want 49 (11*2 + 9*3)", tc.name, total)
			}
			for _, key := range []struct {
				direction sdkmetering.FlowDirection
				component string
			}{
				{sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken},
				{sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken},
			} {
				if amount := wireComponentAmount(t, result.InferenceValuation, key.direction, key.component); amount != nil && amount.Coefficient != "0" {
					t.Fatalf("%s included %s/%s must not be independently payable: %v", tc.name, key.direction, key.component, amount)
				}
			}
		})
	}
}

// TestOpenAIUsageEvidence_CachedReasoningChildOnlyStaysPartial proves the subset
// edge never proves the parent aggregate's complete coverage: a tariff that
// prices only the cached/reasoning subsets leaves the inclusive parents unpriced
// and must stay a typed partial, with the subset lines still payable at their
// literal 20/0 and 21/0 amounts.
func TestOpenAIUsageEvidence_CachedReasoningChildOnlyStaysPartial(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireCacheReasoningChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "child-only-cache"))[0]
	call, leg, policy := wireRetailCall(t, callID, observation)

	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireChildOnlyCachedReasoningRules()))
	if wireOverlapRejected(err) {
		t.Fatalf("child-only cached/reasoning tariff must not be a typed overlap: %v", err)
	}
	if err == nil {
		t.Fatalf("unpriced aggregates cannot falsely complete; total=%s", wireValuationTotal(result.InferenceValuation))
	}
	if result.InferenceValuation.Completeness != economics.CompletenessPartial {
		t.Fatalf("child-only completeness=%q, want partial", result.InferenceValuation.Completeness)
	}
	for _, want := range []struct {
		direction sdkmetering.FlowDirection
		component string
		amount    string
	}{
		{sdkmetering.DirectionInput, sdkmetering.ComponentCacheReadInputToken, "20/0"},
		{sdkmetering.DirectionOutput, sdkmetering.ComponentReasoningOutputToken, "21/0"},
	} {
		amount := wireComponentAmount(t, result.InferenceValuation, want.direction, want.component)
		if amount == nil || amount.CanonicalString() != want.amount {
			t.Fatalf("child-only %s/%s amount=%v, want %s", want.direction, want.component, amount, want.amount)
		}
	}
}

// TestOpenAIUsageEvidence_CachedReasoningAbsentNullZeroNotOverlap covers the
// absent, null and explicit-zero child shapes through the real adapter. Absent
// and null carry no provider quantity; an explicit zero is present but has no
// payable contribution. None may manufacture a priced overlap: the
// aggregate+subset tariff stays complete at the literal aggregate total 49/0.
func TestOpenAIUsageEvidence_CachedReasoningAbsentNullZeroNotOverlap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		promptDetails     string
		completionDetails string
	}{
		{name: "absent", promptDetails: `{}`, completionDetails: `{}`},
		{name: "null", promptDetails: `{"cached_tokens":null}`, completionDetails: `{"reasoning_tokens":null}`},
		{name: "explicit_zero", promptDetails: `{"cached_tokens":0}`, completionDetails: `{"reasoning_tokens":0}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatCacheReasoningUsage(tc.promptDetails, tc.completionDetails), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "shape-"+tc.name))[0]
			call, leg, policy := wireRetailCall(t, callID, observation)

			result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCachedReasoningRules()))
			if wireOverlapRejected(err) {
				t.Fatalf("%s cached/reasoning must not be a priced overlap: %v", tc.name, err)
			}
			if err != nil {
				t.Fatalf("%s aggregate+subset tariff must rate: %v", tc.name, err)
			}
			if result.InferenceValuation.Completeness != economics.CompletenessComplete {
				t.Fatalf("%s completeness=%q, want complete", tc.name, result.InferenceValuation.Completeness)
			}
			if total := wireValuationTotal(result.InferenceValuation); total != "49/0" {
				t.Fatalf("%s total=%s, want 49", tc.name, total)
			}
		})
	}
}

// TestOpenAIUsageEvidence_CachedReasoningUnrecognizedAliasDoesNotDoubleEmit
// proves a conflicting flat compatible-provider spelling does not create a
// second cache-read measure or override the recognized typed
// prompt_tokens_details.cached_tokens value: the single native subset edge still
// classifies the aggregate+subset tariff as a typed overlap.
func TestOpenAIUsageEvidence_CachedReasoningUnrecognizedAliasDoesNotDoubleEmit(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat-cache","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"input_cache_read_tokens":99,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}`
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "alias-conflict"))[0]

	var count int
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentCacheReadInputToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		count++
		if measure.Value == nil || measure.Value.Coefficient != "4" {
			t.Fatalf("cache_read_input_token = %+v, want recognized native 4 (alias must not override)", measure.Value)
		}
	}
	if count != 1 {
		t.Fatalf("conflicting cache alias produced %d cache_read measures, want 1", count)
	}

	call, leg, policy := wireRetailCall(t, callID, observation)
	result, err := rateWireRetail(t, call, leg, policy, wireProviderTariff(t, wireCachedReasoningRules()))
	if !wireOverlapRejected(err) {
		t.Fatalf("aggregate+cached/reasoning err=%v, want typed overlap; total=%s", err, wireValuationTotal(result.InferenceValuation))
	}
}

// TestOpenAIUsageEvidence_CacheWriteWireFieldNowRecognized records the R7-C1E
// reversal of the former residual. The standard Chat
// prompt_tokens_details.cache_write_tokens member is now part of the adapter's
// recognized native wire path (the official SDK type contract exposes it as
// CompletionUsagePromptTokensDetails.cache_write_tokens), so it is surfaced as
// an observed native input cache-write measure and carries a frozen subset
// edge. The recognized cached_tokens member is unaffected.
func TestOpenAIUsageEvidence_CacheWriteWireFieldNowRecognized(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat-cache","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":6},"completion_tokens_details":{"reasoning_tokens":3}}}`
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "cache-write-recognized"))[0]

	var cacheRead int
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentCacheWriteInputToken {
			if measure.Value == nil || measure.Value.Coefficient != "6" || measure.Key.Direction != sdkmetering.DirectionInput {
				t.Fatalf("standard cache_write_tokens measure = %+v, want observed input 6", measure)
			}
		}
		if measure.Key.Component == sdkmetering.ComponentCacheReadInputToken {
			cacheRead++
		}
	}
	if cacheRead != 1 {
		t.Fatalf("recognized cached_tokens measure count=%d, want 1", cacheRead)
	}
}
